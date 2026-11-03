package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Registers the pure-Go "sqlite" driver so the binary keeps building with
	// CGO_ENABLED=0, which the Dockerfile and the PyPI wheels rely on.
	_ "modernc.org/sqlite"
)

// DatabaseFileName is the SQLite database under the state root. Watch targets
// live here; sessions, service config, and hardening state are still JSON
// files and move in later slices of the SQLite migration.
const DatabaseFileName = "vigilante.db"

// legacyWatchlistFileName is the pre-SQLite watch target file. It is imported
// into the database the first time the store opens and then renamed with
// migratedFileSuffix so a downgrade cannot silently read stale data.
const legacyWatchlistFileName = "watchlist.json"

const migratedFileSuffix = ".migrated"

// ErrSchemaTooNew reports a database written by a newer Vigilante than the
// running build. Downgrading needs the data exported first.
var ErrSchemaTooNew = errors.New("state database schema is newer than this vigilante build")

// dbHandle lazily opens the database once per Store and guards that open.
type dbHandle struct {
	mu   sync.Mutex
	conn *sql.DB
}

type migration struct {
	version    int
	statements []string
}

// migrations is applied in order on open. Append new versions; never edit a
// version that has shipped.
var migrations = []migration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE watch_targets (
				position   INTEGER PRIMARY KEY,
				path       TEXT NOT NULL,
				repo       TEXT NOT NULL,
				body       TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX watch_targets_path ON watch_targets (path)`,
			`CREATE INDEX watch_targets_repo ON watch_targets (repo)`,
		},
	},
}

// DatabasePath returns the SQLite database under the state root.
func (s *Store) DatabasePath() string {
	return filepath.Join(s.root, DatabaseFileName)
}

func (s *Store) legacyWatchlistPath() string {
	return filepath.Join(s.root, legacyWatchlistFileName)
}

// Close releases the database connection. Stores that were never used for
// watch targets have nothing to close.
func (s *Store) Close() error {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if s.db.conn == nil {
		return nil
	}
	err := s.db.conn.Close()
	s.db.conn = nil
	return err
}

// database opens the state database on first use, applies pending migrations,
// and imports the legacy watchlist.json once. Later calls reuse the pool.
func (s *Store) database() (*sql.DB, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if s.db.conn != nil {
		return s.db.conn, nil
	}
	for _, dir := range []string{s.root, s.LogsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := openDatabase(s.DatabasePath())
	if err != nil {
		return nil, err
	}
	if err := applyMigrations(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := s.importLegacyWatchlist(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	s.db.conn = conn
	return conn, nil
}

// openDatabase opens path with the pragmas every connection in the pool needs:
// WAL so readers never block the writer, a busy timeout so a second process
// waits instead of failing, and immediate transactions so a read-modify-write
// takes the write lock up front rather than upgrading mid-transaction.
func openDatabase(path string) (*sql.DB, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absPath}
	dsn := uri.String() + "?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := conn.PingContext(context.Background()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open state database %s: %w", absPath, err)
	}
	if err := os.Chmod(absPath, 0o644); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func applyMigrations(conn *sql.DB) error {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	latest := migrations[len(migrations)-1].version
	if current > latest {
		return fmt.Errorf("%w: database is at schema version %d, this build supports up to %d", ErrSchemaTooNew, current, latest)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("apply state schema migration %d: %w", m.version, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.DB, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range m.statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// importLegacyWatchlist moves watchlist.json into watch_targets. It only runs
// while the table is empty, so a database that already has targets is never
// overwritten by a stale file; that file is left alone and logged instead.
func (s *Store) importLegacyWatchlist(conn *sql.DB) error {
	path := s.legacyWatchlistPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var existing int
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM watch_targets`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		s.AppendDaemonLog("legacy %s ignored: %s already holds %d watch targets; remove the file or rename it to keep it", path, s.DatabasePath(), existing)
		return nil
	}
	var targets []WatchTarget
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &targets); err != nil {
			return fmt.Errorf("import legacy watchlist %s: %w", path, err)
		}
	}
	for i := range targets {
		// Legacy migration shim, deliberately pinned to Codex; see the matching
		// note in LoadWatchTargets. Writing the value explicitly here keeps the
		// imported rows self-describing.
		if strings.TrimSpace(targets[i].Provider) == "" {
			targets[i].Provider = "codex"
		}
		targets[i].MaxParallel = normalizeMaxParallelSessions(targets[i].MaxParallel)
	}
	if err := replaceWatchTargets(conn, targets); err != nil {
		return fmt.Errorf("import legacy watchlist %s: %w", path, err)
	}
	if err := os.Rename(path, path+migratedFileSuffix); err != nil {
		return err
	}
	s.AppendDaemonLog("imported %d watch targets from %s into %s", len(targets), path, s.DatabasePath())
	return nil
}

// LoadWatchTargets returns every watch target in the order it was saved.
func (s *Store) LoadWatchTargets() ([]WatchTarget, error) {
	conn, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(context.Background(), `SELECT body FROM watch_targets ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]WatchTarget, 0)
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var target WatchTarget
		if err := json.Unmarshal([]byte(body), &target); err != nil {
			return nil, fmt.Errorf("decode watch target: %w", err)
		}
		// Legacy migration shim, deliberately pinned to Codex: a target with no
		// provider predates Vigilante persisting one, and those were all
		// running on Codex. Do not replace this with provider.DefaultID — that
		// would silently migrate every such repository onto the current
		// default provider. The import path writes the value explicitly, so
		// this only matters for rows saved without a provider since.
		if strings.TrimSpace(target.Provider) == "" {
			target.Provider = "codex"
		}
		target.MaxParallel = normalizeMaxParallelSessions(target.MaxParallel)
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// SaveWatchTargets replaces the stored watch targets with targets, in order,
// inside one transaction.
func (s *Store) SaveWatchTargets(targets []WatchTarget) error {
	for i := range targets {
		targets[i].MaxParallel = normalizeMaxParallelSessions(targets[i].MaxParallel)
	}
	conn, err := s.database()
	if err != nil {
		return err
	}
	return replaceWatchTargets(conn, targets)
}

func replaceWatchTargets(conn *sql.DB, targets []WatchTarget) error {
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM watch_targets`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, target := range targets {
		body, err := json.Marshal(target)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO watch_targets (position, path, repo, body, updated_at) VALUES (?, ?, ?, ?, ?)`, i+1, target.Path, target.Repo, string(body), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
