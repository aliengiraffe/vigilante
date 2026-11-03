package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionJSONWithoutCleanupAttemptsDefaultsToZero(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"repo_path":"/tmp/repo","repo":"owner/repo","issue_number":1}`), &session); err != nil {
		t.Fatal(err)
	}
	if session.CleanupAttempts != 0 {
		t.Fatalf("CleanupAttempts = %d, want 0", session.CleanupAttempts)
	}
}

func TestTryWithScanLockIsExclusive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	stateA := NewStore()
	stateB := NewStore()
	if err := stateA.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	locked, err := stateA.TryWithScanLock(func() error {
		lockedInner, err := stateB.TryWithScanLock(func() error { return nil })
		if err != nil {
			return err
		}
		if lockedInner {
			t.Fatal("expected second scan lock acquisition to fail")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("expected first scan lock acquisition to succeed")
	}
}

func TestAppendLogFileUsesLocalTimezone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("TEST", -8*60*60)
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	path := filepath.Join(t.TempDir(), "vigilante.log")
	appendLogFile(path, "daemon run start")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "-08:00] daemon run start") {
		t.Fatalf("expected local timezone offset in log entry, got %q", text)
	}
	if strings.Contains(text, "Z] daemon run start") {
		t.Fatalf("expected local timezone log entry, got %q", text)
	}
}

func TestSessionLogPathUsesRepositoryQualifiedFilename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath("aliengiraffe/vigilante", 179)
	want := filepath.Join(store.LogsDir(), "aliengiraffe-vigilante-issue-179.log")
	if got != want {
		t.Fatalf("expected repository-qualified session log path %q, got %q", want, got)
	}
}

func TestSessionLogPathSanitizesRepositorySlug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath("Org Name/repo.name:beta", 7)
	want := filepath.Join(store.LogsDir(), "Org-Name-repo-name-beta-issue-7.log")
	if got != want {
		t.Fatalf("expected sanitized session log path %q, got %q", want, got)
	}
}

func TestSessionLogPathDoesNotCollideAcrossRepositories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	first := store.SessionLogPath("owner-one/repo", 7)
	second := store.SessionLogPath("owner-two/repo", 7)
	if first == second {
		t.Fatalf("expected distinct session log paths for shared issue number, got %q", first)
	}
}

func TestSessionLogPathFallsBackWhenRepositorySlugMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()

	got := store.SessionLogPath(" / ", 7)
	want := filepath.Join(store.LogsDir(), "unknown-repo-issue-7.log")
	if got != want {
		t.Fatalf("expected fallback session log path %q, got %q", want, got)
	}
}

func TestSessionStatusClosedRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	sessions := []Session{
		{Repo: "owner/repo", Provider: "claude", Model: "opus", IssueNumber: 1, Branch: "vigilante/issue-1", Status: SessionStatusClosed},
		{Repo: "owner/repo", IssueNumber: 2, Branch: "vigilante/issue-2", Status: SessionStatusSuccess},
	}
	if err := store.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(loaded))
	}
	if loaded[0].Status != SessionStatusClosed {
		t.Fatalf("expected closed status, got %q", loaded[0].Status)
	}
	if loaded[0].Provider != "claude" || loaded[0].Model != "opus" {
		t.Fatalf("expected provider and model to round-trip, got provider=%q model=%q", loaded[0].Provider, loaded[0].Model)
	}
	if loaded[1].Status != SessionStatusSuccess {
		t.Fatalf("expected success status, got %q", loaded[1].Status)
	}
}

func TestWatchTargetMaxParallelDefaultsToSharedValue(t *testing.T) {
	if got := normalizeMaxParallelSessions(0); got != DefaultMaxParallelSessions {
		t.Fatalf("expected zero max_parallel_sessions to normalize to shared default %d, got %d", DefaultMaxParallelSessions, got)
	}
	if got := normalizeMaxParallelSessions(-1); got != 1 {
		t.Fatalf("expected negative max_parallel_sessions to normalize conservatively to 1, got %d", got)
	}
	if got := normalizeMaxParallelSessions(1); got != 1 {
		t.Fatalf("expected explicit max_parallel_sessions to be preserved, got %d", got)
	}
}

func TestEnsureLayoutCreatesDefaultServiceConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.BlockedSessionInactivityTimeout; got != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected default blocked-session timeout %q, got %q", DefaultBlockedSessionInactivityTimeout.String(), got)
	}
}

func TestSaveServiceConfigNormalizesInvalidBlockedSessionTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServiceConfig(ServiceConfig{BlockedSessionInactivityTimeout: "not-a-duration"}); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.BlockedSessionInactivityTimeout; got != DefaultBlockedSessionInactivityTimeout.String() {
		t.Fatalf("expected invalid timeout to normalize to %q, got %q", DefaultBlockedSessionInactivityTimeout.String(), got)
	}
}

func TestIsPackageHardeningEnabledDefault(t *testing.T) {
	config := ServiceConfig{}
	if !config.IsPackageHardeningEnabled() {
		t.Error("package hardening should be enabled by default")
	}
}

func TestIsPackageHardeningEnabledExplicit(t *testing.T) {
	enabled := true
	config := ServiceConfig{PackageHardeningEnabled: &enabled}
	if !config.IsPackageHardeningEnabled() {
		t.Error("expected enabled when explicitly set to true")
	}

	disabled := false
	config = ServiceConfig{PackageHardeningEnabled: &disabled}
	if config.IsPackageHardeningEnabled() {
		t.Error("expected disabled when explicitly set to false")
	}
}

func TestHardeningStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	// Initially empty.
	hs, err := store.LoadHardeningState()
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 0 {
		t.Fatalf("expected empty hardening state, got %d entries", len(hs))
	}

	// Save and reload.
	hs[HardeningPRKey("owner/repo", 42)] = HardeningPRState{
		Repo:          "owner/repo",
		PRNumber:      42,
		CommentID:     12345,
		LabelApplied:  true,
		FindingsCount: 3,
	}
	if err := store.SaveHardeningState(hs); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadHardeningState()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := loaded[HardeningPRKey("owner/repo", 42)]
	if !ok {
		t.Fatal("expected entry for owner/repo#42")
	}
	if entry.PRNumber != 42 || entry.CommentID != 12345 || entry.FindingsCount != 3 {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

func TestHardeningPRKey(t *testing.T) {
	key := HardeningPRKey("owner/repo", 99)
	if key != "owner/repo#99" {
		t.Errorf("key = %q, want owner/repo#99", key)
	}
}

func TestOpenCodeHomeRespectsEnvOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-opencode")
	t.Setenv("OPENCODE_HOME", override)

	store := NewStore()
	if got := store.OpenCodeHome(); got != override {
		t.Fatalf("OpenCodeHome = %q, want %q", got, override)
	}
}

func TestOpenCodeHomeDefaultsToConfigOpencode(t *testing.T) {
	t.Setenv("OPENCODE_HOME", "")

	store := NewStore()
	got := store.OpenCodeHome()
	if !strings.HasSuffix(got, filepath.Join(".config", "opencode")) {
		t.Fatalf("OpenCodeHome = %q, expected suffix .config/opencode", got)
	}
}

func TestServiceConfigPackageHardeningPersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	disabled := false
	if err := store.SaveServiceConfig(ServiceConfig{PackageHardeningEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.IsPackageHardeningEnabled() {
		t.Error("expected package hardening to be disabled after save/load")
	}
}

// TestLoadWatchTargetsBackfillsLegacyProviderAsCodex pins the legacy migration
// shim on the watchlist.json import path and on reads. Watch targets written before
// Vigilante persisted a provider were all running on Codex, so they must keep
// loading as Codex even though the default provider for new targets is now
// Claude.
func TestLoadWatchTargetsBackfillsLegacyProviderAsCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[{"path":"/tmp/repo","repo":"owner/repo"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	targets, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if targets[0].Provider != "codex" {
		t.Fatalf("legacy watch target must stay on codex, got %q", targets[0].Provider)
	}
}

// TestLoadWatchTargetsPreservesExplicitProvider makes sure the legacy backfill
// only fills empty values.
func TestLoadWatchTargetsPreservesExplicitProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[{"path":"/tmp/repo","repo":"owner/repo","provider":"claude"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	targets, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Provider != "claude" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

// TestLoadSessionsBackfillsLegacyProviderAsCodex keeps in-flight sessions on
// the agent that started them. See the LoadWatchTargets counterpart.
func TestLoadSessionsBackfillsLegacyProviderAsCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.sessionsPath(), []byte(`[{"repo":"owner/repo","issue_number":7}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Provider != "codex" {
		t.Fatalf("legacy session must stay on codex, got %q", sessions[0].Provider)
	}
}

func TestCodexSessionModelsSurviveRestart(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	sessions := []Session{{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Model: "gpt-6-astra"}, {Repo: "owner/repo", IssueNumber: 2, Provider: "codex", Model: "gpt-5.6-sol"}, {Repo: "owner/repo", IssueNumber: 3, Provider: "codex"}}
	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore().LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(sessions) {
		t.Fatalf("sessions: %#v", loaded)
	}
	for i, want := range sessions {
		if loaded[i].Provider != want.Provider || loaded[i].Model != want.Model {
			t.Fatalf("session changed: %#v", loaded[i])
		}
	}
}

func TestParseRetentionSupportsDaysAndFallsBack(t *testing.T) {
	fallback := DefaultFailedSessionPollRetention
	cases := map[string]time.Duration{
		"":     fallback,
		"14d":  14 * 24 * time.Hour,
		"1.5d": 36 * time.Hour,
		"336h": 336 * time.Hour,
		"30m":  30 * time.Minute,
		"0":    fallback,
		"-5d":  fallback,
		"junk": fallback,
		" 2D ": 48 * time.Hour,
	}
	for raw, want := range cases {
		if got := ParseRetention(raw, fallback); got != want {
			t.Fatalf("ParseRetention(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestEnsureLayoutCreatesDatabaseInsteadOfWatchlistJSON(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.DatabasePath()); err != nil {
		t.Fatalf("database not created: %v", err)
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); !os.IsNotExist(err) {
		t.Fatalf("fresh layout must not create watchlist.json, stat err=%v", err)
	}
	targets, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("fresh database must have no targets, got %#v", targets)
	}
}

func TestWatchTargetsRoundTripPreservesOrderAndFields(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	want := []WatchTarget{
		{Path: "/repos/beta", Repo: "owner/beta", Branch: "develop", Provider: "claude", Labels: []string{"to-do", "bug"}, MaxParallel: 2, CommentSweep: &CommentSweepCursor{Since: "2026-03-19T11:40:00Z", IssueCommentsETag: `W/"abc"`}},
		{Path: "/repos/alpha", Repo: "owner/alpha", Branch: "main", Provider: "codex", ForkMode: true, ForkOwner: "bot", Assignee: "nico", SandboxMode: true},
	}
	store := NewStore()
	if err := store.SaveWatchTargets(want); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore().LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed targets:\n got %#v\nwant %#v", got, want)
	}

	// Saving again replaces the whole set rather than appending.
	if err := store.SaveWatchTargets(want[1:]); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != "owner/alpha" {
		t.Fatalf("save must replace targets, got %#v", got)
	}
}

func TestImportLegacyWatchlistRenamesFileAndKeepsOrder(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"path":"/repos/b","repo":"owner/b","provider":"claude","max_parallel_sessions":-3},{"path":"/repos/a","repo":"owner/a","provider":"codex"}]`
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Repo != "owner/b" || targets[1].Repo != "owner/a" {
		t.Fatalf("import changed order or count: %#v", targets)
	}
	if targets[0].MaxParallel != 1 {
		t.Fatalf("import must normalize max parallel sessions, got %d", targets[0].MaxParallel)
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy file must be renamed after import, stat err=%v", err)
	}
	migrated, err := os.ReadFile(store.legacyWatchlistPath() + migratedFileSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if string(migrated) != legacy {
		t.Fatalf("migrated copy must keep the original bytes, got %s", migrated)
	}

	// A second store on the same root reads the database, not the file.
	again, err := NewStore().LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, targets) {
		t.Fatalf("second open diverged: %#v vs %#v", again, targets)
	}
}

func TestImportLegacyWatchlistAcceptsEmptyFile(t *testing.T) {
	for _, content := range []string{"", "[]\n"} {
		t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
		store := NewStore()
		if err := os.MkdirAll(store.Root(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.legacyWatchlistPath(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		targets, err := store.LoadWatchTargets()
		if err != nil {
			t.Fatalf("content %q: %v", content, err)
		}
		if len(targets) != 0 {
			t.Fatalf("content %q: unexpected targets %#v", content, targets)
		}
		if _, err := os.Stat(store.legacyWatchlistPath() + migratedFileSuffix); err != nil {
			t.Fatalf("content %q: legacy file not renamed: %v", content, err)
		}
	}
}

func TestImportLegacyWatchlistRejectsMalformedFile(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("malformed legacy watchlist must fail the open instead of being dropped")
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); err != nil {
		t.Fatalf("malformed legacy file must be left in place: %v", err)
	}
}

func TestImportLegacyWatchlistSkipsPopulatedDatabase(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	store := NewStore()
	if err := store.SaveWatchTargets([]WatchTarget{{Path: "/repos/db", Repo: "owner/db", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[{"path":"/repos/stale","repo":"owner/stale"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	targets, err := NewStore().LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Repo != "owner/db" {
		t.Fatalf("populated database must win over a stale legacy file, got %#v", targets)
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); err != nil {
		t.Fatalf("ignored legacy file must be left in place: %v", err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	store := NewStore()
	conn, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, migrations[len(migrations)-1].version+1, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = NewStore().LoadWatchTargets()
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("expected ErrSchemaTooNew, got %v", err)
	}
}

func TestWatchTargetsSurviveConcurrentStores(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))

	// Two stores stand in for the daemon and a CLI command sharing one root.
	stores := []*Store{NewStore(), NewStore()}
	for _, store := range stores {
		if err := store.EnsureLayout(); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				targets := []WatchTarget{{Path: fmt.Sprintf("/repos/%d", i), Repo: fmt.Sprintf("owner/%d-%d", i, n), Provider: "claude"}}
				if err := store.SaveWatchTargets(targets); err != nil {
					errs <- err
					return
				}
				if _, err := store.LoadWatchTargets(); err != nil {
					errs <- err
					return
				}
			}
		}(i, store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent store access failed: %v", err)
	}
	targets, err := NewStore().LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("last writer must leave exactly one target, got %#v", targets)
	}
}
