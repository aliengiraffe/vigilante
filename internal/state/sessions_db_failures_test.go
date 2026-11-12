package state

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenStore returns a store whose state root is a regular file, so every
// database() call fails before SQLite is even opened.
func brokenStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{root: root}
}

// closedStore returns an opened store whose pooled connection has been closed
// underneath it, so the handle resolves but every statement fails.
func closedStore(t *testing.T) *Store {
	t.Helper()
	store, _ := newTestStore(t)
	conn, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return store
}

func execSQL(t *testing.T, store *Store, stmt string, args ...any) {
	t.Helper()
	conn, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func rawSessionRow(t *testing.T, store *Store, issue int, version any, body string) {
	t.Helper()
	execSQL(t, store, `INSERT INTO sessions (repo, issue_number, status, version, body, written_at) VALUES ('owner/repo', ?, 'running', ?, ?, '')`, issue, version, body)
}

// storeCalls lists one call per public store method so the failure tests can
// assert that every one of them surfaces a database error instead of
// returning stale or empty data.
func storeCalls(store *Store) map[string]func() error {
	return map[string]func() error{
		"ListSessions":               func() error { _, err := store.ListSessions(); return err },
		"ListSessionsWithIssueCache": func() error { _, err := store.ListSessionsWithIssueCache(); return err },
		"GetSession":                 func() error { _, _, err := store.GetSession("owner/repo", 1); return err },
		"PutSession":                 func() error { return store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1}) },
		"UpdateSession": func() error {
			return store.UpdateSession("owner/repo", 1, func(*Session) error { return nil })
		},
		"DeleteSession":       func() error { return store.DeleteSession("owner/repo", 1) },
		"ReplaceSessions":     func() error { return store.ReplaceSessions(nil) },
		"LoadSessionSnapshot": func() error { _, _, err := store.LoadSessionSnapshot(true); return err },
		"CommitSessions": func() error {
			_, _, err := store.CommitSessions(&SessionSnapshot{rows: map[string]sessionRow{}}, []Session{{Repo: "owner/repo", IssueNumber: 1}})
			return err
		},
		"LoadServiceConfig":  func() error { _, err := store.LoadServiceConfig(); return err },
		"SaveServiceConfig":  func() error { return store.SaveServiceConfig(ServiceConfig{}) },
		"LoadHardeningState": func() error { _, err := store.LoadHardeningState(); return err },
		"SaveHardeningState": func() error { return store.SaveHardeningState(HardeningState{"k": {}}) },
		"LoadWatchTargets":   func() error { _, err := store.LoadWatchTargets(); return err },
		"SaveWatchTargets":   func() error { return store.SaveWatchTargets([]WatchTarget{{Path: "/p", Repo: "owner/repo"}}) },
		"Export":             func() error { _, err := store.Export(); return err },
		"WriteExport":        func() error { return store.WriteExport(filepath.Join(store.root, "export")) },
	}
}

func TestStoreMethodsFailWhenDatabaseCannotOpen(t *testing.T) {
	store := brokenStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("closing a store that never opened must be a no-op, got %v", err)
	}
	if err := store.EnsureLayout(); err == nil {
		t.Fatal("EnsureLayout must fail when the root is a file")
	}
	for name, call := range storeCalls(store) {
		if err := call(); err == nil {
			t.Errorf("%s: expected an error when the state root is a file", name)
		}
	}
}

func TestStoreMethodsFailOnClosedConnection(t *testing.T) {
	store := closedStore(t)
	for name, call := range storeCalls(store) {
		if err := call(); err == nil {
			t.Errorf("%s: expected an error on a closed connection", name)
		}
	}
}

func TestOpenDatabaseFailsWhenPathIsADirectory(t *testing.T) {
	store := &Store{root: t.TempDir()}
	if err := os.MkdirAll(store.DatabasePath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database(); err == nil {
		t.Fatal("expected open to fail when the database path is a directory")
	}
	if _, err := store.ListSessions(); err == nil {
		t.Fatal("expected every later call to fail too")
	}
}

func TestCommitSessionsRejectsNilSnapshot(t *testing.T) {
	store, _ := newTestStore(t)
	if _, _, err := store.CommitSessions(nil, nil); err == nil {
		t.Fatal("expected an error for a nil snapshot")
	}
}

func TestSessionConflictErrorAndKeyHelpers(t *testing.T) {
	err := &SessionConflictError{Keys: []string{"owner/repo#1", "owner/repo#2"}}
	if got := err.Error(); !strings.Contains(got, ErrSessionConflict.Error()) || !strings.Contains(got, "owner/repo#1, owner/repo#2") {
		t.Fatalf("unexpected message %q", got)
	}
	if repo, issue := splitSessionKey("owner/repo#12"); repo != "owner/repo" || issue != 12 {
		t.Fatalf("splitSessionKey = %q, %d", repo, issue)
	}
	if repo, issue := splitSessionKey("no-hash"); repo != "no-hash" || issue != 0 {
		t.Fatalf("splitSessionKey without a hash = %q, %d", repo, issue)
	}
}

func TestUpdateSessionRejectsKeyChange(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}); err != nil {
		t.Fatal(err)
	}
	err := store.UpdateSession("owner/repo", 1, func(s *Session) error { s.IssueNumber = 2; return nil })
	if err == nil || !strings.Contains(err.Error(), "changed the session key") {
		t.Fatalf("expected key-change error, got %v", err)
	}
	if _, ok, _ := store.GetSession("owner/repo", 2); ok {
		t.Fatal("the rejected update must not have been written")
	}
}

func TestSessionRowsThatCannotBeDecodedSurfaceErrors(t *testing.T) {
	t.Run("malformed body", func(t *testing.T) {
		store, _ := newTestStore(t)
		rawSessionRow(t, store, 1, 1, `{not json`)
		if _, err := store.ListSessions(); err == nil || !strings.Contains(err.Error(), "decode session") {
			t.Fatalf("expected decode error, got %v", err)
		}
		if _, err := store.Export(); err == nil {
			t.Fatal("Export must fail when a session cannot be decoded")
		}
	})
	t.Run("malformed cache", func(t *testing.T) {
		store, _ := newTestStore(t)
		rawSessionRow(t, store, 1, 1, `{"repo":"owner/repo","issue_number":1}`)
		execSQL(t, store, `INSERT INTO session_issue_cache (repo, issue_number, details) VALUES ('owner/repo', 1, '{bad')`)
		if _, err := store.ListSessions(); err != nil {
			t.Fatalf("listing without the cache must still work: %v", err)
		}
		if _, err := store.ListSessionsWithIssueCache(); err == nil || !strings.Contains(err.Error(), "decode session issue cache") {
			t.Fatalf("expected cache decode error, got %v", err)
		}
	})
	t.Run("unscannable version", func(t *testing.T) {
		store, _ := newTestStore(t)
		rawSessionRow(t, store, 1, "not-a-number", `{"repo":"owner/repo","issue_number":1}`)
		if _, err := store.ListSessions(); err == nil {
			t.Fatal("expected a scan error for a non-integer version")
		}
	})
}

func TestConfigHardeningAndWatchTargetDecodeFailures(t *testing.T) {
	store, _ := newTestStore(t)

	execSQL(t, store, `INSERT INTO hardening_pr_state (key, repo, pr_number, body, updated_at) VALUES ('owner/repo#1', 'owner/repo', 1, '{bad', '')`)
	if _, err := store.LoadHardeningState(); err == nil || !strings.Contains(err.Error(), "decode hardening state") {
		t.Fatalf("expected hardening decode error, got %v", err)
	}
	if _, err := store.Export(); err == nil {
		t.Fatal("Export must fail when hardening state cannot be decoded")
	}
	execSQL(t, store, `DELETE FROM hardening_pr_state`)

	// A TEXT primary key may be NULL in SQLite; scanning it into a string fails.
	execSQL(t, store, `INSERT INTO hardening_pr_state (key, repo, pr_number, body, updated_at) VALUES (NULL, 'owner/repo', 1, '{}', '')`)
	if _, err := store.LoadHardeningState(); err == nil {
		t.Fatal("expected a scan error for a NULL hardening key")
	}
	execSQL(t, store, `DELETE FROM hardening_pr_state`)

	execSQL(t, store, `INSERT INTO service_config (id, body, updated_at) VALUES (1, '{bad', '')`)
	if _, err := store.LoadServiceConfig(); err == nil || !strings.Contains(err.Error(), "decode service config") {
		t.Fatalf("expected config decode error, got %v", err)
	}
	if _, err := store.Export(); err == nil {
		t.Fatal("Export must fail when the config cannot be decoded")
	}
	execSQL(t, store, `DELETE FROM service_config`)

	execSQL(t, store, `INSERT INTO watch_targets (position, path, repo, body, updated_at) VALUES (1, '/p', 'owner/repo', '{bad', '')`)
	if _, err := store.LoadWatchTargets(); err == nil || !strings.Contains(err.Error(), "decode watch target") {
		t.Fatalf("expected watch target decode error, got %v", err)
	}
	if _, err := store.Export(); err == nil {
		t.Fatal("Export must fail when a watch target cannot be decoded")
	}
	if err := store.WriteExport(t.TempDir()); err == nil {
		t.Fatal("WriteExport must fail when Export fails")
	}
}

func TestWriteExportFailsWhenDirectoryIsAFile(t *testing.T) {
	store, _ := newTestStore(t)
	dir := filepath.Join(t.TempDir(), "export")
	if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteExport(dir); err == nil {
		t.Fatal("expected WriteExport to fail when the target is a file")
	}
}

func TestStoreMethodsFailWhenTablesAreMissing(t *testing.T) {
	withCache := Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning, CachedIssueDetails: cachedDetails("one")}

	t.Run("session_issue_cache", func(t *testing.T) {
		store, _ := newTestStore(t)
		if err := store.PutSession(withCache); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 2, Provider: "claude", Status: SessionStatusRunning}); err != nil {
			t.Fatal(err)
		}
		snap, sessions, err := store.LoadSessionSnapshot(true)
		if err != nil {
			t.Fatal(err)
		}
		// Another writer bumps every version so each compare-and-swap in the
		// commit misses and falls through to re-reading the row.
		execSQL(t, store, `UPDATE sessions SET version = version + 1`)
		execSQL(t, store, `DROP TABLE session_issue_cache`)

		if _, _, err := store.GetSession("owner/repo", 1); err == nil {
			t.Error("GetSession must fail without the cache table")
		}
		if err := store.UpdateSession("owner/repo", 1, func(*Session) error { return nil }); err == nil {
			t.Error("UpdateSession must fail without the cache table")
		}
		if _, _, err := store.LoadSessionSnapshot(false); err == nil {
			t.Error("LoadSessionSnapshot must fail without the cache table")
		}
		if err := store.PutSession(withCache); err == nil {
			t.Error("PutSession with a cache must fail without the cache table")
		}
		if err := store.DeleteSession("owner/repo", 2); err == nil {
			t.Error("DeleteSession must fail when the cache row cannot be removed")
		}
		// Dropping issue 2 from the slice is a delete whose version no longer
		// matches, so the commit re-reads the row and hits the missing table.
		if _, _, err := store.CommitSessions(snap, sessions[:1]); err == nil {
			t.Error("CommitSessions must fail when a conflicting delete cannot be re-read")
		}
		sessions[0].IssueTitle = "changed"
		if _, _, err := store.CommitSessions(snap, sessions); err == nil {
			t.Error("CommitSessions must fail when the conflicting row cannot be re-read")
		}
	})

	t.Run("session_issue_cache delete after a clean session delete", func(t *testing.T) {
		store, _ := newTestStore(t)
		if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}); err != nil {
			t.Fatal(err)
		}
		snap, _, err := store.LoadSessionSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, store, `DROP TABLE session_issue_cache`)
		if _, _, err := store.CommitSessions(snap, nil); err == nil {
			t.Fatal("CommitSessions must fail when the cache row of a deleted session cannot be removed")
		}
	})

	t.Run("sessions", func(t *testing.T) {
		store, _ := newTestStore(t)
		if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}); err != nil {
			t.Fatal(err)
		}
		snap, sessions, err := store.LoadSessionSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, store, `DROP TABLE sessions`)
		if err := store.ReplaceSessions(nil); err == nil {
			t.Error("ReplaceSessions must fail without the sessions table")
		}
		if err := store.PutSession(Session{Repo: "owner/repo", IssueNumber: 1}); err == nil {
			t.Error("PutSession must fail without the sessions table")
		}
		if err := store.DeleteSession("owner/repo", 1); err == nil {
			t.Error("DeleteSession must fail without the sessions table")
		}
		if _, _, err := store.CommitSessions(snap, nil); err == nil {
			t.Error("CommitSessions must fail when the delete statement fails")
		}
		sessions[0].IssueTitle = "changed"
		if _, _, err := store.CommitSessions(snap, sessions); err == nil {
			t.Error("CommitSessions must fail when the update statement fails")
		}
		if _, err := store.Export(); err == nil {
			t.Error("Export must fail without the sessions table")
		}
	})

	t.Run("service_config", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `DROP TABLE service_config`)
		if _, err := store.LoadServiceConfig(); err == nil {
			t.Error("LoadServiceConfig must fail without its table")
		}
		if err := store.SaveServiceConfig(ServiceConfig{}); err == nil {
			t.Error("SaveServiceConfig must fail without its table")
		}
	})

	t.Run("hardening_pr_state", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `DROP TABLE hardening_pr_state`)
		if _, err := store.LoadHardeningState(); err == nil {
			t.Error("LoadHardeningState must fail without its table")
		}
		if err := store.SaveHardeningState(HardeningState{}); err == nil {
			t.Error("SaveHardeningState must fail without its table")
		}
	})

	t.Run("watch_targets", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `DROP TABLE watch_targets`)
		if _, err := store.LoadWatchTargets(); err == nil {
			t.Error("LoadWatchTargets must fail without its table")
		}
		if err := store.SaveWatchTargets(nil); err == nil {
			t.Error("SaveWatchTargets must fail without its table")
		}
	})
}

// A RAISE trigger makes one statement fail while the rest of the schema keeps
// working, which is the only way to reach the write-error branches that sit
// after a successful read or delete in the same transaction.
func TestStoreWritesSurfaceStatementFailures(t *testing.T) {
	plain := Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning}
	withCache := plain
	withCache.CachedIssueDetails = cachedDetails("one")

	t.Run("session insert", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `CREATE TRIGGER block BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		if err := store.PutSession(plain); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("PutSession: expected the trigger error, got %v", err)
		}
		if err := store.ReplaceSessions([]Session{plain}); err == nil {
			t.Error("ReplaceSessions must surface the insert failure")
		}
		snap, _, err := store.LoadSessionSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CommitSessions(snap, []Session{plain}); err == nil {
			t.Error("CommitSessions must surface the insert failure")
		}
	})

	t.Run("session update", func(t *testing.T) {
		store, _ := newTestStore(t)
		if err := store.PutSession(plain); err != nil {
			t.Fatal(err)
		}
		snap, sessions, err := store.LoadSessionSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, store, `CREATE TRIGGER block BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		if err := store.UpdateSession("owner/repo", 1, func(s *Session) error { s.IssueTitle = "x"; return nil }); err == nil {
			t.Error("UpdateSession must surface the update failure")
		}
		sessions[0].IssueTitle = "changed"
		if _, _, err := store.CommitSessions(snap, sessions); err == nil {
			t.Error("CommitSessions must surface the update failure")
		}
	})

	t.Run("session cache insert", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `CREATE TRIGGER block BEFORE INSERT ON session_issue_cache BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		if err := store.PutSession(withCache); err == nil {
			t.Error("PutSession must surface the cache insert failure")
		}
		snap, _, err := store.LoadSessionSnapshot(true)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CommitSessions(snap, []Session{withCache}); err == nil {
			t.Error("CommitSessions must surface the cache insert failure")
		}
	})

	t.Run("session row vanishes after insert", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `CREATE TRIGGER vanish AFTER INSERT ON sessions BEGIN DELETE FROM sessions WHERE repo = NEW.repo AND issue_number = NEW.issue_number; END`)
		err := store.PutSession(plain)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("expected the version read-back to report no rows, got %v", err)
		}
	})

	t.Run("hardening insert", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `CREATE TRIGGER block BEFORE INSERT ON hardening_pr_state BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		if err := store.SaveHardeningState(HardeningState{"owner/repo#1": {Repo: "owner/repo", PRNumber: 1}}); err == nil {
			t.Error("SaveHardeningState must surface the insert failure")
		}
	})

	t.Run("watch target insert", func(t *testing.T) {
		store, _ := newTestStore(t)
		execSQL(t, store, `CREATE TRIGGER block BEFORE INSERT ON watch_targets BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		if err := store.SaveWatchTargets([]WatchTarget{{Path: "/p", Repo: "owner/repo"}}); err == nil {
			t.Error("SaveWatchTargets must surface the insert failure")
		}
	})
}

func TestCommitSessionsDeleteConflicts(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.ReplaceSessions([]Session{
		{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning},
		{Repo: "owner/repo", IssueNumber: 2, Provider: "claude", Status: SessionStatusRunning},
		{Repo: "owner/repo", IssueNumber: 3, Provider: "claude", Status: SessionStatusRunning},
	}); err != nil {
		t.Fatal(err)
	}
	snap, _, err := store.LoadSessionSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	other := NewStore()
	if err := other.UpdateSession("owner/repo", 1, func(s *Session) error { s.Status = SessionStatusBlocked; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := other.DeleteSession("owner/repo", 2); err != nil {
		t.Fatal(err)
	}

	// The caller drops all three; 1 changed underneath (kept, reported), 2 was
	// already gone (reported, dropped from the snapshot), 3 is deleted.
	committed, conflicts, err := store.CommitSessions(snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 2 || !containsString(conflicts, "owner/repo#1") || !containsString(conflicts, "owner/repo#2") {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if len(committed) != 1 || committed[0].IssueNumber != 1 || committed[0].Status != SessionStatusBlocked {
		t.Fatalf("expected the concurrently changed session back from the database, got %#v", committed)
	}
	stored, _ := store.ListSessions()
	if len(stored) != 1 || stored[0].IssueNumber != 1 {
		t.Fatalf("expected only issue 1 to survive, got %#v", stored)
	}

	// The snapshot now knows the database's copy of 1, so dropping it again
	// succeeds without a conflict.
	committed, conflicts, err = store.CommitSessions(snap, nil)
	if err != nil || len(conflicts) != 0 || len(committed) != 0 {
		t.Fatalf("second delete: committed=%#v conflicts=%v err=%v", committed, conflicts, err)
	}
	if stored, _ := store.ListSessions(); len(stored) != 0 {
		t.Fatalf("expected no sessions, got %#v", stored)
	}
}

func TestCommitSessionsWritesAndReplacesIssueCache(t *testing.T) {
	store, _ := newTestStore(t)
	snap, sessions, err := store.LoadSessionSnapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{Repo: "owner/repo", IssueNumber: 1, Provider: "claude", Status: SessionStatusRunning, CachedIssueDetails: cachedDetails("first")}
	sessions = append(sessions, session)
	if _, conflicts, err := store.CommitSessions(snap, sessions); err != nil || len(conflicts) != 0 {
		t.Fatalf("insert with cache: conflicts=%v err=%v", conflicts, err)
	}
	loaded, _ := store.ListSessionsWithIssueCache()
	if len(loaded) != 1 || loaded[0].CachedIssueDetails == nil || loaded[0].CachedIssueDetails.Details.Title != "first" {
		t.Fatalf("cache not written on insert: %#v", loaded)
	}

	sessions[0].CachedIssueDetails = cachedDetails("second")
	if _, conflicts, err := store.CommitSessions(snap, sessions); err != nil || len(conflicts) != 0 {
		t.Fatalf("update with cache: conflicts=%v err=%v", conflicts, err)
	}
	loaded, _ = store.ListSessionsWithIssueCache()
	if loaded[0].CachedIssueDetails.Details.Title != "second" {
		t.Fatalf("cache not replaced on update: %#v", loaded[0].CachedIssueDetails)
	}

	// A commit that changes nothing writes nothing, cache included.
	if _, conflicts, err := store.CommitSessions(snap, sessions); err != nil || len(conflicts) != 0 {
		t.Fatalf("no-op commit: conflicts=%v err=%v", conflicts, err)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestApplyMigrationsFailures(t *testing.T) {
	t.Run("closed connection", func(t *testing.T) {
		conn, err := openDatabase(filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if err := applyMigrations(conn); err == nil {
			t.Fatal("expected the bookkeeping table creation to fail")
		}
		if err := applyMigration(context.Background(), conn, migrations[0]); err == nil {
			t.Fatal("expected the migration transaction to fail to begin")
		}
	})

	t.Run("bookkeeping table without a version column", func(t *testing.T) {
		conn, err := openDatabase(filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Exec(`CREATE TABLE schema_migrations (other INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := applyMigrations(conn); err == nil {
			t.Fatal("expected the version query to fail")
		}
	})

	t.Run("migration statement fails", func(t *testing.T) {
		conn, err := openDatabase(filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// A table from migration 1 that exists without a schema_migrations row
		// makes that migration fail on open, which is what a half-applied
		// schema looks like.
		if _, err := conn.Exec(`CREATE TABLE watch_targets (position INTEGER PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		err = applyMigrations(conn)
		if err == nil || !strings.Contains(err.Error(), "apply state schema migration 1") {
			t.Fatalf("expected migration 1 to fail, got %v", err)
		}
	})

	t.Run("bookkeeping insert fails", func(t *testing.T) {
		conn, err := openDatabase(filepath.Join(t.TempDir(), "x.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := applyMigrations(conn); err != nil {
			t.Fatal(err)
		}
		bad := migration{version: 99, statements: []string{`DROP TABLE schema_migrations`}}
		if err := applyMigration(context.Background(), conn, bad); err == nil {
			t.Fatal("expected the version bookkeeping insert to fail")
		}
		var count int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(migrations) {
			t.Fatalf("failed migration must roll back: count=%d err=%v", count, err)
		}
	})
}

func TestImportLegacyFilesFailures(t *testing.T) {
	t.Run("closed connection", func(t *testing.T) {
		store := closedStore(t)
		if err := store.importLegacyFiles(store.db.conn); err == nil {
			t.Fatal("expected the import transaction to fail to begin")
		}
	})

	t.Run("legacy path is a directory", func(t *testing.T) {
		t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
		store := NewStore()
		if err := os.MkdirAll(store.legacySessionsPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := store.database(); err == nil {
			t.Fatal("expected open to fail when sessions.json cannot be read")
		}
	})

	t.Run("malformed hardening state", func(t *testing.T) {
		t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
		store := NewStore()
		if err := os.MkdirAll(store.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.legacyHardeningPath(), []byte(`{bad`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.database(); err == nil {
			t.Fatal("expected open to fail on malformed hardening_state.json")
		}
	})

	t.Run("table missing", func(t *testing.T) {
		store, _ := newTestStore(t)
		conn, err := store.database()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[]`), 0o644); err != nil {
			t.Fatal(err)
		}
		execSQL(t, store, `DROP TABLE watch_targets`)
		if err := store.importLegacyFiles(conn); err == nil {
			t.Fatal("expected the row count query to fail")
		}
	})

	t.Run("rename fails", func(t *testing.T) {
		t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
		store := NewStore()
		if err := os.MkdirAll(store.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.legacySessionsPath(), []byte(`[]`), 0o644); err != nil {
			t.Fatal(err)
		}
		// A non-empty directory at the migrated name makes the rename fail.
		if err := os.MkdirAll(filepath.Join(store.legacySessionsPath()+migratedFileSuffix, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := store.database(); err == nil {
			t.Fatal("expected open to fail when the imported file cannot be renamed")
		}
	})
}
