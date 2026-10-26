package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	return store
}

func cachedDetails(title string) *CachedIssueDetails {
	return &CachedIssueDetails{FetchedAt: "2026-10-26T10:00:00Z", Details: backend.WorkItemDetails{Title: title, Body: "body", Labels: []backend.Label{{Name: "to-do"}}}}
}

func TestSessionsRoundTripKeepsInsertionOrderAndSplitsCache(t *testing.T) {
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
		store.legacyConfigPath():    `{"github_background_budget": 7, "blocked_session_inactivity_timeout": "45m"}`,
		store.legacyHardeningPath(): `{"owner/repo#3": {"repo":"owner/repo","pr_number":3,"comment_id":5}}`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
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
		t.Fatalf("config not imported: %#v", config)
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
	for _, name := range []string{legacyWatchlistFileName, legacyConfigFileName, legacyHardeningFileName} {
		if _, err := os.Stat(filepath.Join(exportDir, name)); err != nil {
			t.Fatalf("export missing %s: %v", name, err)
		}
	}
}

func TestImportLegacyConfigSkipsMalformedFileButKeepsOpening(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyConfigPath(), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatalf("malformed config.json must not block the open: %v", err)
	}
	if config.BlockedSessionInactivityTimeout != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected defaults, got %#v", config)
	}
	if _, err := os.Stat(store.legacyConfigPath()); err != nil {
		t.Fatalf("skipped config.json must be left in place: %v", err)
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

func TestConfigFileOverridesStoredConfigAndIsConsumed(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveServiceConfig(ServiceConfig{GitHubBackgroundBudget: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyConfigPath(), []byte(`{"github_background_budget": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := NewStore().LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GitHubBackgroundBudget != 9 {
		t.Fatalf("config.json must override the stored config, got %#v", config)
	}
	if _, err := os.Stat(store.legacyConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("applied config.json must be renamed, stat err=%v", err)
	}
	if _, err := os.Stat(store.legacyConfigPath() + migratedFileSuffix); err != nil {
		t.Fatalf("migrated copy missing: %v", err)
	}
}
