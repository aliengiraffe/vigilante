package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore points VIGILANTE_HOME at a fresh temp root and returns a store
// whose database is already open, closing it when the test ends.
func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	conn, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, conn
}

// closedDatabase returns a migrated connection that has already been closed,
// so every call on it fails.
func closedDatabase(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := openDatabase(filepath.Join(t.TempDir(), DatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestCloseIsSafeWithoutOpenDatabase(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close must be a no-op, got %v", err)
	}
}

func TestDatabaseReusesOpenConnection(t *testing.T) {
	store, conn := newTestStore(t)
	again, err := store.database()
	if err != nil {
		t.Fatal(err)
	}
	if again != conn {
		t.Fatal("database must reuse the connection pool it already opened")
	}
}

func TestWatchTargetsFailWhenRootIsAFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".vigilante")
	if err := os.WriteFile(root, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIGILANTE_HOME", root)

	store := NewStore()
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("load must fail when the state root cannot be created")
	}
	if err := store.SaveWatchTargets([]WatchTarget{{Path: "/repos/a", Repo: "owner/a"}}); err == nil {
		t.Fatal("save must fail when the state root cannot be created")
	}
	if err := store.EnsureLayout(); err == nil {
		t.Fatal("ensure layout must fail when the state root cannot be created")
	}
}

func TestDatabaseOpenFailsWhenDatabasePathIsADirectory(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.DatabasePath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("open must fail when the database path is a directory")
	}
	if err := store.EnsureLayout(); err == nil {
		t.Fatal("ensure layout must surface the database open failure")
	}
}

func TestDatabaseOpenFailsOnCorruptFile(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := []byte(strings.Repeat("this is not a sqlite database file ", 64))
	if err := os.WriteFile(store.DatabasePath(), garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("open must fail on a file that is not a SQLite database")
	}
}

func TestApplyMigrationsReportsFailingVersion(t *testing.T) {
	original := migrations
	t.Cleanup(func() { migrations = original })
	migrations = append(append([]migration{}, original...), migration{
		version:    original[len(original)-1].version + 1,
		statements: []string{`THIS IS NOT SQL`},
	})

	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	_, err := NewStore().LoadWatchTargets()
	if err == nil || !strings.Contains(err.Error(), "apply state schema migration") {
		t.Fatalf("expected a failing migration error, got %v", err)
	}
}

func TestApplyMigrationRejectsAlreadyRecordedVersion(t *testing.T) {
	_, conn := newTestStore(t)
	if err := applyMigration(context.Background(), conn, migration{version: migrations[0].version}); err == nil {
		t.Fatal("recording an applied version twice must fail")
	}
}

func TestMigrationHelpersFailOnClosedDatabase(t *testing.T) {
	conn := closedDatabase(t)
	if err := applyMigrations(conn); err == nil {
		t.Fatal("applyMigrations must fail on a closed database")
	}
	if err := applyMigration(context.Background(), conn, migrations[0]); err == nil {
		t.Fatal("applyMigration must fail on a closed database")
	}
	if err := replaceWatchTargets(conn, []WatchTarget{{Path: "/repos/a", Repo: "owner/a"}}); err == nil {
		t.Fatal("replaceWatchTargets must fail on a closed database")
	}
}

func TestImportLegacyWatchlistFailsWhenCountFails(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.importLegacyWatchlist(closedDatabase(t)); err == nil {
		t.Fatal("import must fail when the target count cannot be read")
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); err != nil {
		t.Fatalf("legacy file must be left in place after a failed import: %v", err)
	}
}

func TestImportLegacyWatchlistFailsWhenFileIsUnreadable(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := os.MkdirAll(store.legacyWatchlistPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("open must fail when watchlist.json cannot be read")
	}
}

func TestImportLegacyWatchlistFailsWhenInsertFails(t *testing.T) {
	store, conn := newTestStore(t)
	if _, err := conn.Exec(`CREATE TRIGGER reject_watch_targets BEFORE INSERT ON watch_targets BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[{"path":"/repos/a","repo":"owner/a"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore().LoadWatchTargets()
	if err == nil || !strings.Contains(err.Error(), "import legacy watchlist") {
		t.Fatalf("expected an import failure, got %v", err)
	}
	if _, err := os.Stat(store.legacyWatchlistPath()); err != nil {
		t.Fatalf("legacy file must be left in place after a failed import: %v", err)
	}
}

func TestImportLegacyWatchlistFailsWhenRenameFails(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	// A non-empty directory at the rename destination makes os.Rename fail.
	blocker := filepath.Join(store.legacyWatchlistPath()+migratedFileSuffix, "keep")
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyWatchlistPath(), []byte(`[{"path":"/repos/a","repo":"owner/a"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("open must fail when the legacy file cannot be renamed")
	}
}

func TestWatchTargetsFailWhenTableIsMissing(t *testing.T) {
	store, conn := newTestStore(t)
	if _, err := conn.Exec(`DROP TABLE watch_targets`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("load must fail when watch_targets is missing")
	}
	if err := store.SaveWatchTargets([]WatchTarget{{Path: "/repos/a", Repo: "owner/a"}}); err == nil {
		t.Fatal("save must fail when watch_targets is missing")
	}
}

func TestSaveWatchTargetsFailsWhenInsertIsRejected(t *testing.T) {
	store, conn := newTestStore(t)
	if err := store.SaveWatchTargets([]WatchTarget{{Path: "/repos/keep", Repo: "owner/keep", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TRIGGER reject_watch_targets BEFORE INSERT ON watch_targets BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]WatchTarget{{Path: "/repos/new", Repo: "owner/new"}}); err == nil {
		t.Fatal("save must fail when the insert is rejected")
	}
	// The failed save rolls back, so the previous targets survive.
	targets, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Repo != "owner/keep" {
		t.Fatalf("failed save must roll back, got %#v", targets)
	}
}

func TestLoadWatchTargetsRejectsUndecodableBody(t *testing.T) {
	store, conn := newTestStore(t)
	if _, err := conn.Exec(`INSERT INTO watch_targets (position, path, repo, body, updated_at) VALUES (1, '/repos/a', 'owner/a', '{not json', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_, err := store.LoadWatchTargets()
	if err == nil || !strings.Contains(err.Error(), "decode watch target") {
		t.Fatalf("expected a decode error, got %v", err)
	}
}

func TestLoadWatchTargetsRejectsNullBody(t *testing.T) {
	store, conn := newTestStore(t)
	// Recreate the table without NOT NULL so a NULL body reaches Scan.
	for _, stmt := range []string{
		`DROP TABLE watch_targets`,
		`CREATE TABLE watch_targets (position INTEGER PRIMARY KEY, path TEXT, repo TEXT, body TEXT, updated_at TEXT)`,
		`INSERT INTO watch_targets (position, path, repo, body, updated_at) VALUES (1, '/repos/a', 'owner/a', NULL, '2026-01-01T00:00:00Z')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.LoadWatchTargets(); err == nil {
		t.Fatal("load must fail when a body cannot be scanned")
	}
}
