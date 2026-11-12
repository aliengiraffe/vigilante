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

// DatabaseFileName is the SQLite database under the state root. Watch
// targets, sessions, the per-session issue cache, service config, and
// hardening state all live here.
const DatabaseFileName = "vigilante.db"

// Legacy file names from the JSON layout. Each is imported into the database
// the first time the store opens and then renamed with migratedFileSuffix so a
// downgrade cannot silently read stale data.
const (
	legacyWatchlistFileName = "watchlist.json"
	legacySessionsFileName  = "sessions.json"
	legacyConfigFileName    = "config.json"
	legacyHardeningFileName = "hardening_state.json"
)

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
	{
		version: 2,
		statements: []string{
			// body is the Session record without cached_issue_details; the
			// promoted columns exist for queries and indexes only, body stays
			// the source of truth. version is bumped on every write so a
			// writer holding a stale copy can detect it (see CommitSessions).
			`CREATE TABLE sessions (
				repo                 TEXT NOT NULL,
				issue_number         INTEGER NOT NULL,
				status               TEXT NOT NULL,
				started_at           TEXT NOT NULL DEFAULT '',
				updated_at           TEXT NOT NULL DEFAULT '',
				next_comment_poll_at TEXT NOT NULL DEFAULT '',
				version              INTEGER NOT NULL DEFAULT 1,
				body                 TEXT NOT NULL,
				written_at           TEXT NOT NULL,
				PRIMARY KEY (repo, issue_number)
			)`,
			`CREATE INDEX sessions_status ON sessions (status)`,
			// The cached issue details are most of a session's bytes and are
			// only needed by the scan loop, so they live apart from the body.
			`CREATE TABLE session_issue_cache (
				repo         TEXT NOT NULL,
				issue_number INTEGER NOT NULL,
				fetched_at   TEXT NOT NULL DEFAULT '',
				details      TEXT NOT NULL,
				PRIMARY KEY (repo, issue_number)
			)`,
			`CREATE TABLE service_config (
				id         INTEGER PRIMARY KEY CHECK (id = 1),
				body       TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE hardening_pr_state (
				key        TEXT PRIMARY KEY,
				repo       TEXT NOT NULL,
				pr_number  INTEGER NOT NULL,
				body       TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
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

func (s *Store) legacySessionsPath() string {
	return filepath.Join(s.root, legacySessionsFileName)
}

func (s *Store) legacyConfigPath() string {
	return filepath.Join(s.root, legacyConfigFileName)
}

func (s *Store) legacyHardeningPath() string {
	return filepath.Join(s.root, legacyHardeningFileName)
}

// Close releases the database connection. Stores that were never opened have
// nothing to close.
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
// and imports the legacy JSON files once. Later calls reuse the pool.
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
	if err := s.importLegacyFiles(conn); err != nil {
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

// legacyImport describes one JSON file to fold into the database: its path,
// the table that must still be empty for the import to run, and the importer
// that parses the bytes and writes rows inside the shared transaction. A
// strict importer fails the open on malformed data so nothing is dropped;
// a lenient one (service config, which has safe defaults) logs and skips.
type legacyImport struct {
	path   string
	table  string
	strict bool
	// overwrite imports the file even when the table already has rows. The
	// service config uses it: writing config.json and restarting the daemon
	// stays the way operators change settings, and the file is consumed
	// (renamed) once applied.
	overwrite bool
	load      func(ctx context.Context, q dbExecer, data []byte) (int, error)
}

// importLegacyFiles moves the pre-SQLite JSON files into the database inside
// one transaction, then renames each imported file so it is not read again.
// A table that already holds rows is never overwritten by a data file; the
// file is left alone and logged instead. config.json is the exception (see
// legacyImport.overwrite).
func (s *Store) importLegacyFiles(conn *sql.DB) error {
	imports := []legacyImport{
		{path: s.legacyWatchlistPath(), table: "watch_targets", strict: true, load: importLegacyWatchlistData},
		{path: s.legacySessionsPath(), table: "sessions", strict: true, load: importLegacySessionsData},
		{path: s.legacyConfigPath(), table: "service_config", strict: false, overwrite: true, load: importLegacyConfigData},
		{path: s.legacyHardeningPath(), table: "hardening_pr_state", strict: true, load: importLegacyHardeningData},
	}
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var imported []string
	var notes []string
	for _, imp := range imports {
		data, err := os.ReadFile(imp.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+imp.table).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 && !imp.overwrite {
			notes = append(notes, fmt.Sprintf("legacy %s ignored: %s already holds %d %s rows; remove the file or rename it to keep it", imp.path, s.DatabasePath(), existing, imp.table))
			continue
		}
		count, err := imp.load(ctx, tx, data)
		if err != nil {
			if imp.strict {
				return fmt.Errorf("import legacy %s: %w", imp.path, err)
			}
			notes = append(notes, fmt.Sprintf("legacy %s skipped: %v; defaults apply until the file is fixed or removed", imp.path, err))
			continue
		}
		imported = append(imported, imp.path)
		notes = append(notes, fmt.Sprintf("imported %d %s rows from %s into %s", count, imp.table, imp.path, s.DatabasePath()))
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, path := range imported {
		if err := os.Rename(path, path+migratedFileSuffix); err != nil {
			return err
		}
	}
	for _, note := range notes {
		s.AppendDaemonLog("%s", note)
	}
	return nil
}

func importLegacyWatchlistData(ctx context.Context, q dbExecer, data []byte) (int, error) {
	var targets []WatchTarget
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &targets); err != nil {
			return 0, err
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
	return len(targets), replaceWatchTargetRows(ctx, q, targets)
}

func importLegacySessionsData(ctx context.Context, q dbExecer, data []byte) (int, error) {
	var sessions []Session
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &sessions); err != nil {
			return 0, err
		}
	}
	for i := range sessions {
		// Legacy migration shim, deliberately pinned to Codex; see decodeSession.
		if strings.TrimSpace(sessions[i].Provider) == "" {
			sessions[i].Provider = "codex"
		}
	}
	return len(sessions), replaceSessionRows(ctx, q, sessions)
}

func importLegacyConfigData(ctx context.Context, q dbExecer, data []byte) (int, error) {
	config := defaultServiceConfig()
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return 0, err
		}
	}
	return 1, saveServiceConfigRow(ctx, q, config)
}

func importLegacyHardeningData(ctx context.Context, q dbExecer, data []byte) (int, error) {
	hs := make(HardeningState)
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &hs); err != nil {
			return 0, err
		}
	}
	return len(hs), replaceHardeningRows(ctx, q, hs)
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
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceWatchTargetRows(ctx, tx, targets); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceWatchTargetRows(ctx context.Context, q dbExecer, targets []WatchTarget) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM watch_targets`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, target := range targets {
		body, err := json.Marshal(target)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO watch_targets (position, path, repo, body, updated_at) VALUES (?, ?, ?, ?, ?)`, i+1, target.Path, target.Repo, string(body), now); err != nil {
			return err
		}
	}
	return nil
}
