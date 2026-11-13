package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
)

func cachedDetails(title string) *CachedIssueDetails {
	return &CachedIssueDetails{FetchedAt: "2026-10-26T10:00:00Z", Details: backend.WorkItemDetails{Title: title, Body: "body", Labels: []backend.Label{{Name: "to-do"}}}}
}

func TestSessionsRoundTripKeepsInsertionOrderAndSplitsCache(t *testing.T) {
	store, _ := newTestStore(t)
	want := []Session{
		{Repo: "owner/b", IssueNumber: 9, Provider: "claude", Status: SessionStatusRunning, CIRemediationAttempts: []CIAttempt{{Observation: "o", Checks: "c", AttemptedAt: "t"}}, CachedIssueDetails: cachedDetails("nine")},
		{Repo: "owner/a", IssueNumber: 1, Provider: "codex", Status: SessionStatusBlocked, BlockedReason: BlockedReason{Kind: "git_auth"}},
	}
	if err := store.ReplaceSessions(want); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].IssueNumber != 9 || listed[1].IssueNumber != 1 {
		t.Fatalf("order not preserved: %#v", listed)
	}
	if listed[0].CachedIssueDetails != nil {
		t.Fatal("ListSessions must not load the issue cache")
	}

	withCache, err := store.ListSessionsWithIssueCache()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withCache, want) {
		t.Fatalf("round trip with cache changed sessions:\n got %#v\nwant %#v", withCache, want)
	}

	var cacheRows int
	conn, _ := store.database()
	if err := conn.QueryRow(`SELECT COUNT(*) FROM session_issue_cache`).Scan(&cacheRows); err != nil {
		t.Fatal(err)
	}
	if cacheRows != 1 {
		t.Fatalf("expected one cache row, got %d", cacheRows)
	}
	var body string
	if err := conn.QueryRow(`SELECT body FROM sessions WHERE issue_number = 9`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if json.Valid([]byte(body)) == false || containsKey(body, "cached_issue_details") {
		t.Fatalf("session body must not carry the cache: %s", body)
	}
}

func containsKey(body string, key string) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &m)
	_, ok := m[key]
	return ok
}

func TestPutGetUpdateDeleteSession(t *testing.T) {
	store, _ := newTestStore(t)
	session := Session{Repo: "owner/repo", IssueNumber: 4, Provider: "claude", Status: SessionStatusRunning}
	if err := store.PutSession(session); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetSession("owner/repo", 4)
	if err != nil || !ok || got.Status != SessionStatusRunning {
		t.Fatalf("GetSession = %#v, %v, %v", got, ok, err)
	}
	if _, ok, _ := store.GetSession("owner/repo", 5); ok {
		t.Fatal("missing session reported as present")
	}

	if err := store.UpdateSession("owner/repo", 4, func(s *Session) error {
		s.Status = SessionStatusSuccess
		s.CachedIssueDetails = cachedDetails("four")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	withCache, err := store.ListSessionsWithIssueCache()
	if err != nil {
		t.Fatal(err)
	}
	if withCache[0].Status != SessionStatusSuccess || withCache[0].CachedIssueDetails == nil || withCache[0].CachedIssueDetails.Details.Title != "four" {
		t.Fatalf("update did not persist status and cache: %#v", withCache[0])
	}

	// A nil cache on a later write leaves the stored cache alone.
	if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 4, Provider: "claude", Status: SessionStatusClosed}); err != nil {
		t.Fatal(err)
	}
	withCache, _ = store.ListSessionsWithIssueCache()
	if withCache[0].Status != SessionStatusClosed || withCache[0].CachedIssueDetails == nil {
		t.Fatalf("nil cache must not clear the stored cache: %#v", withCache[0])
	}

	errSkip := errors.New("skip")
	if err := store.UpdateSession("owner/repo", 4, func(s *Session) error { s.Status = SessionStatusFailed; return errSkip }); !errors.Is(err, errSkip) {
		t.Fatalf("fn error must be returned, got %v", err)
	}
	got, _, _ = store.GetSession("owner/repo", 4)
	if got.Status != SessionStatusClosed {
		t.Fatalf("aborted update must not persist, got %q", got.Status)
	}
	if err := store.UpdateSession("owner/repo", 99, func(*Session) error { return nil }); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
	if err := store.DeleteSession("owner/repo", 4); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession("owner/repo", 4); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound on second delete, got %v", err)
	}
	sessions, _ := store.ListSessions()
	if len(sessions) != 0 {
		t.Fatalf("delete left sessions behind: %#v", sessions)
	}
}

func TestCommitSessionsWritesOnlyChangedRowsAndDetectsConflicts(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.ReplaceSessions([]Session{
		{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning},
		{Repo: "owner/repo", IssueNumber: 2, Provider: "claude", Status: SessionStatusBlocked},
		{Repo: "owner/repo", IssueNumber: 3, Provider: "claude", Status: SessionStatusSuccess},
	}); err != nil {
		t.Fatal(err)
	}

	// The "scan" reads a snapshot, then a "CLI command" in another store
	// resumes issue 2 before the scan commits its own view of issue 2.
	scan, sessions, err := store.LoadSessionSnapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	cli := NewStore()
	if err := cli.UpdateSession("owner/repo", 2, func(s *Session) error { s.Status = SessionStatusResuming; s.ResumeHint = "cli"; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := range sessions {
		switch sessions[i].IssueNumber {
		case 1:
			sessions[i].Status = SessionStatusSuccess
		case 2:
			sessions[i].LastMaintenanceError = "scan view is stale"
		}
	}
	sessions = append(sessions, Session{Repo: "owner/repo", IssueNumber: 4, Provider: "claude", Status: SessionStatusRunning})

	committed, conflicts, err := store.CommitSessions(scan, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"owner/repo#2"}) {
		t.Fatalf("conflicts = %v", conflicts)
	}
	byIssue := map[int]Session{}
	for _, s := range committed {
		byIssue[s.IssueNumber] = s
	}
	if byIssue[2].Status != SessionStatusResuming || byIssue[2].ResumeHint != "cli" || byIssue[2].LastMaintenanceError != "" {
		t.Fatalf("conflicted session must come back as the database has it: %#v", byIssue[2])
	}
	stored, _ := store.ListSessions()
	if len(stored) != 4 {
		t.Fatalf("expected 4 sessions after commit, got %#v", stored)
	}
	for _, s := range stored {
		switch s.IssueNumber {
		case 1:
			if s.Status != SessionStatusSuccess {
				t.Fatalf("scan change to issue 1 lost: %#v", s)
			}
		case 2:
			if s.Status != SessionStatusResuming || s.LastMaintenanceError != "" {
				t.Fatalf("CLI change to issue 2 overwritten: %#v", s)
			}
		case 4:
			if s.Status != SessionStatusRunning {
				t.Fatalf("new session not inserted: %#v", s)
			}
		}
	}

	// Versions: 1 and 4 written once by the commit, 2 by the CLI, 3 untouched.
	conn, _ := store.database()
	versions := map[int]int64{}
	rows, err := conn.Query(`SELECT issue_number, version FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		var v int64
		if err := rows.Scan(&n, &v); err != nil {
			t.Fatal(err)
		}
		versions[n] = v
	}
	rows.Close()
	if versions[1] != 2 || versions[2] != 2 || versions[3] != 1 || versions[4] != 1 {
		t.Fatalf("unexpected versions %v", versions)
	}

	// The snapshot now reflects the committed rows, so a second commit with
	// no changes writes nothing and a change to issue 2 succeeds.
	committed[0].IssueTitle = "renamed"
	_, conflicts, err = store.CommitSessions(scan, committed)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("second commit: conflicts=%v err=%v", conflicts, err)
	}
	got, _, _ := store.GetSession("owner/repo", 1)
	if got.IssueTitle != "renamed" {
		t.Fatalf("second commit lost the change: %#v", got)
	}

	// Removing a session from the slice deletes it.
	_, conflicts, err = store.CommitSessions(scan, committed[1:])
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("delete commit: conflicts=%v err=%v", conflicts, err)
	}
	if _, ok, _ := store.GetSession("owner/repo", 1); ok {
		t.Fatal("session removed from the slice must be deleted")
	}

	var conflictErr *SessionConflictError
	err = &SessionConflictError{Keys: []string{"a#1"}}
	if !errors.As(err, &conflictErr) || !errors.Is(err, ErrSessionConflict) {
		t.Fatal("SessionConflictError must unwrap to ErrSessionConflict")
	}
}

func TestCommitSessionsDropsSessionDeletedUnderneath(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}); err != nil {
		t.Fatal(err)
	}
	snap, sessions, err := store.LoadSessionSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore().DeleteSession("owner/repo", 1); err != nil {
		t.Fatal(err)
	}
	sessions[0].Status = SessionStatusSuccess
	committed, conflicts, err := store.CommitSessions(snap, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || len(committed) != 0 {
		t.Fatalf("expected the deleted session to be dropped: committed=%#v conflicts=%v", committed, conflicts)
	}
}

func TestConcurrentStoresNeverLoseSessionWrites(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.ReplaceSessions([]Session{{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}}); err != nil {
		t.Fatal(err)
	}
	const writers = 4
	const perWriter = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle := NewStore()
			for i := 0; i < perWriter; i++ {
				if err := handle.UpdateSession("owner/repo", 1, func(s *Session) error { s.CleanupAttempts++; return nil }); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent update failed: %v", err)
	}
	got, _, _ := store.GetSession("owner/repo", 1)
	if got.CleanupAttempts != writers*perWriter {
		t.Fatalf("lost updates: CleanupAttempts = %d, want %d", got.CleanupAttempts, writers*perWriter)
	}
}

func TestServiceConfigAndHardeningRoundTripThroughDatabase(t *testing.T) {
	store, _ := newTestStore(t)
	disabled := false
	backups := 3
	if err := store.SaveServiceConfig(ServiceConfig{GitHubBackgroundBudget: 42, SandboxEnabled: &disabled, LogMaxBackups: &backups}); err != nil {
		t.Fatal(err)
	}
	config, err := NewStore().LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHubBackgroundBudget != 42 || config.SandboxEnabled == nil || *config.SandboxEnabled || config.LogMaxBackups == nil || *config.LogMaxBackups != 3 {
		t.Fatalf("config did not round trip: %#v", config)
	}
	if config.BlockedSessionInactivityTimeout != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("missing timeout must fall back to the default, got %q", config.BlockedSessionInactivityTimeout)
	}

	hs := HardeningState{HardeningPRKey("owner/repo", 7): {Repo: "owner/repo", PRNumber: 7, CommentID: 11, LabelApplied: true}}
	if err := store.SaveHardeningState(hs); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore().LoadHardeningState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, hs) {
		t.Fatalf("hardening state changed: %#v", loaded)
	}
	if err := store.SaveHardeningState(HardeningState{}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = store.LoadHardeningState(); len(loaded) != 0 {
		t.Fatalf("save must replace the whole state, got %#v", loaded)
	}
}

func TestImportLegacyFilesMovesEverythingAndRenames(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacySessions := []Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: SessionStatusSuccess, CachedIssueDetails: cachedDetails("one")},
		{Repo: "owner/repo", IssueNumber: 2, Provider: "claude", Status: SessionStatusFailed},
	}
	sessionsJSON, _ := json.Marshal(legacySessions)
	files := map[string]string{
		store.legacyWatchlistPath(): `[{"path":"/repos/a","repo":"owner/repo","provider":"claude"}]`,
		store.legacySessionsPath():  string(sessionsJSON),
		store.legacyHardeningPath(): `{"owner/repo#3": {"repo":"owner/repo","pr_number":3,"comment_id":5}}`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configJSON := `{"github_background_budget": 7, "blocked_session_inactivity_timeout": "45m"}`
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	for path := range files {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s must be renamed after import", filepath.Base(path))
		}
		if _, err := os.Stat(path + migratedFileSuffix); err != nil {
			t.Fatalf("%s migrated copy missing: %v", filepath.Base(path), err)
		}
	}
	targets, _ := store.LoadWatchTargets()
	if len(targets) != 1 || targets[0].Repo != "owner/repo" {
		t.Fatalf("watch targets not imported: %#v", targets)
	}
	sessions, err := store.ListSessionsWithIssueCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].Provider != "codex" || sessions[1].Provider != "claude" {
		t.Fatalf("sessions not imported with the provider shim: %#v", sessions)
	}
	if sessions[0].CachedIssueDetails == nil || sessions[0].CachedIssueDetails.Details.Title != "one" {
		t.Fatalf("cache not split out on import: %#v", sessions[0])
	}
	config, _ := store.LoadServiceConfig()
	if config.GitHubBackgroundBudget != 7 || config.BlockedSessionInactivityTimeout != "45m0s" {
		t.Fatalf("config.json not read: %#v", config)
	}
	if data, err := os.ReadFile(store.ServiceConfigPath()); err != nil || string(data) != configJSON {
		t.Fatalf("config.json must stay in place unchanged, got %q err=%v", data, err)
	}
	if _, err := os.Stat(store.ServiceConfigPath() + migratedFileSuffix); !os.IsNotExist(err) {
		t.Fatalf("config.json must not be renamed, stat err=%v", err)
	}
	hs, _ := store.LoadHardeningState()
	if hs["owner/repo#3"].CommentID != 5 {
		t.Fatalf("hardening state not imported: %#v", hs)
	}

	// Export reproduces the legacy shapes with the cache inlined again.
	exportDir := t.TempDir()
	if err := store.WriteExport(exportDir); err != nil {
		t.Fatal(err)
	}
	var exported []Session
	data, err := os.ReadFile(filepath.Join(exportDir, legacySessionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	legacySessions[0].Provider = "codex"
	if !reflect.DeepEqual(exported, legacySessions) {
		t.Fatalf("export differs from import:\n got %#v\nwant %#v", exported, legacySessions)
	}
	for _, name := range []string{legacyWatchlistFileName, ServiceConfigFileName, legacyHardeningFileName} {
		if _, err := os.Stat(filepath.Join(exportDir, name)); err != nil {
			t.Fatalf("export missing %s: %v", name, err)
		}
	}
	var exportedConfig ServiceConfig
	data, err = os.ReadFile(filepath.Join(exportDir, ServiceConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &exportedConfig); err != nil {
		t.Fatal(err)
	}
	if exportedConfig.GitHubBackgroundBudget != 7 {
		t.Fatalf("export must carry the effective config from config.json, got %#v", exportedConfig)
	}
}

func TestLoadServiceConfigRejectsMalformedFile(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.LoadServiceConfig()
	if err == nil || !strings.Contains(err.Error(), store.ServiceConfigPath()) {
		t.Fatalf("malformed config.json must fail with an error naming the file, got %v", err)
	}
	// Other state is unaffected by a broken config file.
	if _, err := store.LoadWatchTargets(); err != nil {
		t.Fatalf("store must still open: %v", err)
	}
	if data, err := os.ReadFile(store.ServiceConfigPath()); err != nil || string(data) != `{not json` {
		t.Fatalf("malformed config.json must be left in place, got %q err=%v", data, err)
	}
}

func TestImportLegacySessionsRejectsMalformedFile(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacySessionsPath(), []byte(`[{"repo":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessions(); err == nil {
		t.Fatal("malformed sessions.json must fail the open instead of being dropped")
	}
}

func TestOpenUpgradesVersionOneDatabase(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Build a database exactly as the previous release left it: schema
	// version 1 with a watch target, and sessions.json still on disk.
	conn, err := openDatabase(store.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, conn, migrations[0]); err != nil {
		t.Fatal(err)
	}
	if err := replaceWatchTargetRows(ctx, conn, []WatchTarget{{Path: "/repos/a", Repo: "owner/repo", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacySessionsPath(), []byte(`[{"repo":"owner/repo","issue_number":1,"provider":"claude","status":"running"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != SessionStatusRunning {
		t.Fatalf("sessions.json not imported after upgrade: %#v", sessions)
	}
	targets, _ := store.LoadWatchTargets()
	if len(targets) != 1 {
		t.Fatalf("watch targets lost on upgrade: %#v", targets)
	}
	db, _ := store.database()
	var version sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version.Int64 != int64(migrations[len(migrations)-1].version) {
		t.Fatalf("schema version = %d", version.Int64)
	}
}

func TestConfigFileIsReadInPlaceOnEveryLoad(t *testing.T) {
	store, _ := newTestStore(t)
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.BlockedSessionInactivityTimeout != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("missing config.json must mean defaults, got %#v", config)
	}
	if _, err := os.Stat(store.ServiceConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("loading defaults must not create config.json, stat err=%v", err)
	}
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(`{"github_background_budget": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if config, err = store.LoadServiceConfig(); err != nil || config.GitHubBackgroundBudget != 9 {
		t.Fatalf("config.json not read, got %#v err=%v", config, err)
	}
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(`{"github_background_budget": 3, "blocked_session_inactivity_timeout": "nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if config, err = NewStore().LoadServiceConfig(); err != nil || config.GitHubBackgroundBudget != 3 {
		t.Fatalf("edited config.json not applied, got %#v err=%v", config, err)
	}
	if config.BlockedSessionInactivityTimeout != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("invalid timeout must normalize to the default, got %q", config.BlockedSessionInactivityTimeout)
	}
	if _, err := os.Stat(store.ServiceConfigPath() + migratedFileSuffix); !os.IsNotExist(err) {
		t.Fatalf("config.json must never be consumed, stat err=%v", err)
	}
}

func TestSaveServiceConfigWritesFileAtomically(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.SaveServiceConfig(ServiceConfig{GitHubBackgroundBudget: 4, BlockedSessionInactivityTimeout: "90m"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.ServiceConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new config.json mode = %v, want 0600", info.Mode().Perm())
	}
	config, err := store.LoadServiceConfig()
	if err != nil || config.GitHubBackgroundBudget != 4 || config.BlockedSessionInactivityTimeout != "1h30m0s" {
		t.Fatalf("saved config not read back, got %#v err=%v", config, err)
	}
	if err := os.Chmod(store.ServiceConfigPath(), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServiceConfig(ServiceConfig{GitHubBackgroundBudget: 5}); err != nil {
		t.Fatal(err)
	}
	if info, err = os.Stat(store.ServiceConfigPath()); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("rewrite must keep the existing mode, got %v err=%v", info.Mode().Perm(), err)
	}
}

// seedVersionTwoDatabase builds a database as #546 left it: schema version 2
// with service config stored in the service_config table. body is the row, or
// "" for no row.
func seedVersionTwoDatabase(t *testing.T, store *Store, body string) {
	t.Helper()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	conn, err := openDatabase(store.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:2] {
		if err := applyMigration(ctx, conn, m); err != nil {
			t.Fatal(err)
		}
	}
	if body != "" {
		if _, err := conn.ExecContext(ctx, `INSERT INTO service_config (id, body, updated_at) VALUES (1, ?, ?)`, body, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func assertServiceConfigTableDropped(t *testing.T, store *Store) {
	t.Helper()
	db, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'service_config'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("service_config table must be dropped")
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}
}

func TestUpgradeRestoresConfigFileFromServiceConfigRow(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	seedVersionTwoDatabase(t, store, `{"github_background_budget":11,"sandbox_enabled":true}`)
	// A stale consumed copy from the #546 flow must be ignored and kept.
	migrated := store.ServiceConfigPath() + migratedFileSuffix
	if err := os.WriteFile(migrated, []byte(`{"github_background_budget":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHubBackgroundBudget != 11 || !config.IsSandboxEnabled() {
		t.Fatalf("row values not restored: %#v", config)
	}
	info, err := os.Stat(store.ServiceConfigPath())
	if err != nil {
		t.Fatalf("config.json must be restored: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("restored config.json mode = %v, want 0600", info.Mode().Perm())
	}
	assertServiceConfigTableDropped(t, store)
	if data, err := os.ReadFile(migrated); err != nil || string(data) != `{"github_background_budget":1}` {
		t.Fatalf("config.json.migrated must be left untouched, got %q err=%v", data, err)
	}
	logData, err := os.ReadFile(store.DaemonLogPath())
	if err != nil || !strings.Contains(string(logData), "restored service config") || !strings.Contains(string(logData), store.ServiceConfigPath()) {
		t.Fatalf("daemon log must record the restore, got %q err=%v", logData, err)
	}

	// Re-opening does nothing further.
	restored, _ := os.ReadFile(store.ServiceConfigPath())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	again := NewStore()
	if _, err := again.LoadServiceConfig(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(again.ServiceConfigPath()); string(data) != string(restored) {
		t.Fatalf("re-open must not rewrite config.json:\n got %q\nwant %q", data, restored)
	}
	assertServiceConfigTableDropped(t, again)
}

func TestUpgradeKeepsExistingConfigFileOverServiceConfigRow(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	seedVersionTwoDatabase(t, store, `{"github_background_budget":11}`)
	const fileJSON = "{\"github_background_budget\": 2}\n"
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(fileJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHubBackgroundBudget != 2 {
		t.Fatalf("existing config.json must win, got %#v", config)
	}
	info, err := os.Stat(store.ServiceConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(store.ServiceConfigPath()); string(data) != fileJSON || info.Mode().Perm() != 0o644 {
		t.Fatalf("existing config.json must be untouched, got %q mode %v", data, info.Mode().Perm())
	}
	assertServiceConfigTableDropped(t, store)
}

func TestUpgradeWithoutServiceConfigRowCreatesNoFile(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	seedVersionTwoDatabase(t, store, "")
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.BlockedSessionInactivityTimeout != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected defaults, got %#v", config)
	}
	if _, err := os.Stat(store.ServiceConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("no row means no config.json, stat err=%v", err)
	}
	assertServiceConfigTableDropped(t, store)
}

func TestUpgradeRecoversFromCrashBetweenRestoreAndDrop(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	seedVersionTwoDatabase(t, store, `{"github_background_budget":11}`)
	// Simulate a crash after the restore wrote config.json but before
	// migration 3 dropped the table: run only the restore step.
	conn, err := openDatabase(store.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.restoreServiceConfigFile(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// The operator edits the restored file before the next start.
	const edited = "{\"github_background_budget\": 12}\n"
	if err := os.WriteFile(store.ServiceConfigPath(), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := NewStore().LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHubBackgroundBudget != 12 {
		t.Fatalf("config.json must not be overwritten after a crash, got %#v", config)
	}
	if data, _ := os.ReadFile(store.ServiceConfigPath()); string(data) != edited {
		t.Fatalf("config.json changed on recovery: %q", data)
	}
	assertServiceConfigTableDropped(t, NewStore())
}
