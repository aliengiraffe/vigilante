package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrSessionNotFound reports an UpdateSession or DeleteSession call for a
// session that is not stored.
var ErrSessionNotFound = errors.New("session not found")

// ErrSessionConflict reports that a session changed in the database after the
// caller read it, so the caller's copy was not written. The caller should
// re-read the session and decide again rather than overwrite the newer row.
var ErrSessionConflict = errors.New("session changed since it was read")

// SessionConflictError lists the sessions a CommitSessions call refused to
// overwrite. It unwraps to ErrSessionConflict.
type SessionConflictError struct {
	Keys []string
}

func (e *SessionConflictError) Error() string {
	return fmt.Sprintf("%v: %s", ErrSessionConflict, strings.Join(e.Keys, ", "))
}

// Unwrap lets errors.Is match ErrSessionConflict.
func (e *SessionConflictError) Unwrap() error { return ErrSessionConflict }

// SessionKey is the map key both the store and the app use for one session.
func SessionKey(repo string, issue int) string {
	return fmt.Sprintf("%s#%d", repo, issue)
}

// sessionRow is what the store knows about a session row beyond its decoded
// body: the version used for compare-and-swap writes, the stored body and
// cache text used to skip unchanged rows, and the rowid that keeps listing
// order equal to insertion order.
type sessionRow struct {
	version int64
	body    string
	cache   string
}

// SessionSnapshot remembers the version of every session a caller read so a
// later CommitSessions can write back only the rows that changed and detect
// rows that another process or goroutine changed in between. A snapshot is
// bound to one caller; it is safe to commit repeatedly.
type SessionSnapshot struct {
	mu        sync.Mutex
	withCache bool
	rows      map[string]sessionRow
}

// dbExecer is the subset of *sql.DB and *sql.Tx the store needs.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const sessionSelectColumns = `s.version, s.body, c.details`

const sessionSelectFrom = ` FROM sessions s LEFT JOIN session_issue_cache c ON c.repo = s.repo AND c.issue_number = s.issue_number`

// encodeSession splits a session into its stored body (never carrying the
// issue cache) and the cache document, which is empty when the session does
// not carry one.
func encodeSession(session Session) (body string, cache string, err error) {
	cached := session.CachedIssueDetails
	session.CachedIssueDetails = nil
	data, err := json.Marshal(session)
	if err != nil {
		return "", "", err
	}
	if cached == nil {
		return string(data), "", nil
	}
	cacheData, err := json.Marshal(cached)
	if err != nil {
		return "", "", err
	}
	return string(data), string(cacheData), nil
}

func decodeSession(body string, cache string) (Session, error) {
	var session Session
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	if cache != "" {
		var cached CachedIssueDetails
		if err := json.Unmarshal([]byte(cache), &cached); err != nil {
			return Session{}, fmt.Errorf("decode session issue cache: %w", err)
		}
		session.CachedIssueDetails = &cached
	}
	// Legacy migration shim, deliberately pinned to Codex; see the matching
	// note in LoadWatchTargets. In-flight sessions recorded before the
	// provider field existed must keep running on the agent that started
	// them, so this must not become provider.DefaultID.
	if strings.TrimSpace(session.Provider) == "" {
		session.Provider = "codex"
	}
	return session, nil
}

func querySessions(ctx context.Context, q dbExecer, withCache bool, where string, args ...any) ([]Session, map[string]sessionRow, error) {
	columns := sessionSelectColumns
	if !withCache {
		columns = `s.version, s.body, ''`
	}
	query := `SELECT ` + columns + sessionSelectFrom
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY s.rowid`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	sessions := make([]Session, 0)
	index := make(map[string]sessionRow)
	for rows.Next() {
		var row sessionRow
		var cache sql.NullString
		if err := rows.Scan(&row.version, &row.body, &cache); err != nil {
			return nil, nil, err
		}
		row.cache = cache.String
		session, err := decodeSession(row.body, row.cache)
		if err != nil {
			return nil, nil, err
		}
		sessions = append(sessions, session)
		index[SessionKey(session.Repo, session.IssueNumber)] = row
	}
	return sessions, index, rows.Err()
}

// ListSessions returns every session in insertion order without the issue
// details cache; CachedIssueDetails is nil on each result.
func (s *Store) ListSessions() ([]Session, error) {
	conn, err := s.database()
	if err != nil {
		return nil, err
	}
	sessions, _, err := querySessions(context.Background(), conn, false, "")
	return sessions, err
}

// ListSessionsWithIssueCache is ListSessions with CachedIssueDetails filled
// in for every session that has a cached issue. The scan loop needs it; status
// views do not.
func (s *Store) ListSessionsWithIssueCache() ([]Session, error) {
	conn, err := s.database()
	if err != nil {
		return nil, err
	}
	sessions, _, err := querySessions(context.Background(), conn, true, "")
	return sessions, err
}

// GetSession returns one session without its issue cache. The boolean reports
// whether it exists.
func (s *Store) GetSession(repo string, issue int) (Session, bool, error) {
	conn, err := s.database()
	if err != nil {
		return Session{}, false, err
	}
	sessions, _, err := querySessions(context.Background(), conn, false, `s.repo = ? AND s.issue_number = ?`, repo, issue)
	if err != nil {
		return Session{}, false, err
	}
	if len(sessions) == 0 {
		return Session{}, false, nil
	}
	return sessions[0], true, nil
}

// PutSession inserts or replaces one session unconditionally. When the
// session carries CachedIssueDetails the cache row is written too; a nil
// cache leaves any stored cache row untouched.
func (s *Store) PutSession(session Session) error {
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
	if _, err := upsertSessionRow(ctx, tx, session); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateSession applies fn to the stored session inside one immediate
// transaction, so no other writer can interleave between the read and the
// write. fn must not perform I/O: the database write lock is held while it
// runs. An error from fn aborts the update and is returned unchanged.
// ErrSessionNotFound is returned when the session does not exist.
func (s *Store) UpdateSession(repo string, issue int, fn func(*Session) error) error {
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
	sessions, _, err := querySessions(ctx, tx, false, `s.repo = ? AND s.issue_number = ?`, repo, issue)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return ErrSessionNotFound
	}
	session := sessions[0]
	if err := fn(&session); err != nil {
		return err
	}
	if session.Repo != repo || session.IssueNumber != issue {
		return fmt.Errorf("update session %s: fn changed the session key to %s", SessionKey(repo, issue), SessionKey(session.Repo, session.IssueNumber))
	}
	if _, err := upsertSessionRow(ctx, tx, session); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteSession removes one session and its cache row. Deleting a session that
// does not exist returns ErrSessionNotFound.
func (s *Store) DeleteSession(repo string, issue int) error {
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
	result, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE repo = ? AND issue_number = ?`, repo, issue)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrSessionNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_issue_cache WHERE repo = ? AND issue_number = ?`, repo, issue); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceSessions discards every stored session and cache row and stores
// sessions in order. It exists for the legacy importer and for test seeding;
// application code changes sessions one row at a time.
func (s *Store) ReplaceSessions(sessions []Session) error {
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
	if err := replaceSessionRows(ctx, tx, sessions); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceSessionRows(ctx context.Context, q dbExecer, sessions []Session) error {
	for _, stmt := range []string{`DELETE FROM sessions`, `DELETE FROM session_issue_cache`} {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, session := range sessions {
		if _, err := upsertSessionRow(ctx, q, session); err != nil {
			return err
		}
	}
	return nil
}

// upsertSessionRow writes session unconditionally and returns the version the
// row has afterwards.
func upsertSessionRow(ctx context.Context, q dbExecer, session Session) (int64, error) {
	body, cache, err := encodeSession(session)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := q.ExecContext(ctx, `INSERT INTO sessions (repo, issue_number, status, started_at, updated_at, next_comment_poll_at, version, body, written_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (repo, issue_number) DO UPDATE SET
			status = excluded.status,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at,
			next_comment_poll_at = excluded.next_comment_poll_at,
			version = sessions.version + 1,
			body = excluded.body,
			written_at = excluded.written_at`,
		session.Repo, session.IssueNumber, string(session.Status), session.StartedAt, session.UpdatedAt, session.NextCommentPollAt, body, now); err != nil {
		return 0, err
	}
	if cache != "" {
		if err := upsertSessionCacheRow(ctx, q, session.Repo, session.IssueNumber, session.CachedIssueDetails.FetchedAt, cache); err != nil {
			return 0, err
		}
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM sessions WHERE repo = ? AND issue_number = ?`, session.Repo, session.IssueNumber).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func upsertSessionCacheRow(ctx context.Context, q dbExecer, repo string, issue int, fetchedAt string, cache string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO session_issue_cache (repo, issue_number, fetched_at, details)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (repo, issue_number) DO UPDATE SET fetched_at = excluded.fetched_at, details = excluded.details`,
		repo, issue, fetchedAt, cache)
	return err
}

// LoadSessionSnapshot reads every session, with the issue cache when
// includeIssueCache is set, and returns a snapshot that CommitSessions uses to
// write back only what changed.
func (s *Store) LoadSessionSnapshot(includeIssueCache bool) (*SessionSnapshot, []Session, error) {
	conn, err := s.database()
	if err != nil {
		return nil, nil, err
	}
	sessions, rows, err := querySessions(context.Background(), conn, includeIssueCache, "")
	if err != nil {
		return nil, nil, err
	}
	return &SessionSnapshot{withCache: includeIssueCache, rows: rows}, sessions, nil
}

// CommitSessions writes the sessions that differ from what snap last read, in
// one immediate transaction, using the version each row had when it was read.
// A row that another writer changed or removed in the meantime is not
// overwritten: it is reported in the returned conflict keys, and the returned
// slice carries the database's current copy of that session (or drops it when
// it was deleted) so the caller continues from the truth. Sessions missing from
// the slice but present in the snapshot are deleted under the same rule. After
// a successful commit the snapshot reflects the written rows, so the caller can
// keep mutating and commit again.
func (s *Store) CommitSessions(snap *SessionSnapshot, sessions []Session) ([]Session, []string, error) {
	if snap == nil {
		return nil, nil, errors.New("commit sessions: nil snapshot")
	}
	conn, err := s.database()
	if err != nil {
		return nil, nil, err
	}
	snap.mu.Lock()
	defer snap.mu.Unlock()
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	result := make([]Session, 0, len(sessions))
	conflicts := []string{}
	seen := make(map[string]bool, len(sessions))
	pending := make(map[string]sessionRow)
	for _, session := range sessions {
		key := SessionKey(session.Repo, session.IssueNumber)
		seen[key] = true
		body, cache, err := encodeSession(session)
		if err != nil {
			return nil, nil, err
		}
		prev, known := snap.rows[key]
		if known && body == prev.body && (cache == "" || cache == prev.cache) {
			result = append(result, session)
			continue
		}
		written, err := writeSessionCAS(ctx, tx, session, body, cache, prev, known)
		if err != nil {
			return nil, nil, err
		}
		if !written {
			conflicts = append(conflicts, key)
			current, exists, err := loadSessionRow(ctx, tx, session.Repo, session.IssueNumber, snap.withCache)
			if err != nil {
				return nil, nil, err
			}
			if exists {
				result = append(result, current.session)
				pending[key] = current.row
			} else {
				delete(snap.rows, key)
			}
			continue
		}
		version := prev.version + 1
		if !known {
			version = 1
		}
		next := sessionRow{version: version, body: body, cache: prev.cache}
		if cache != "" {
			next.cache = cache
		}
		pending[key] = next
		result = append(result, session)
	}
	for key, prev := range snap.rows {
		if seen[key] {
			continue
		}
		repo, issue := splitSessionKey(key)
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE repo = ? AND issue_number = ? AND version = ?`, repo, issue, prev.version)
		if err != nil {
			return nil, nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			conflicts = append(conflicts, key)
			current, exists, err := loadSessionRow(ctx, tx, repo, issue, snap.withCache)
			if err != nil {
				return nil, nil, err
			}
			if exists {
				result = append(result, current.session)
				pending[key] = current.row
				continue
			}
			delete(snap.rows, key)
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_issue_cache WHERE repo = ? AND issue_number = ?`, repo, issue); err != nil {
			return nil, nil, err
		}
		delete(snap.rows, key)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	for key, row := range pending {
		snap.rows[key] = row
	}
	return result, conflicts, nil
}

// writeSessionCAS writes session only if the row still has the version the
// snapshot read (or does not exist yet, for a new session). It reports whether
// the write happened.
func writeSessionCAS(ctx context.Context, q dbExecer, session Session, body string, cache string, prev sessionRow, known bool) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var res sql.Result
	var err error
	if known {
		res, err = q.ExecContext(ctx, `UPDATE sessions SET status = ?, started_at = ?, updated_at = ?, next_comment_poll_at = ?, version = version + 1, body = ?, written_at = ?
			WHERE repo = ? AND issue_number = ? AND version = ?`,
			string(session.Status), session.StartedAt, session.UpdatedAt, session.NextCommentPollAt, body, now,
			session.Repo, session.IssueNumber, prev.version)
	} else {
		res, err = q.ExecContext(ctx, `INSERT INTO sessions (repo, issue_number, status, started_at, updated_at, next_comment_poll_at, version, body, written_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT (repo, issue_number) DO NOTHING`,
			session.Repo, session.IssueNumber, string(session.Status), session.StartedAt, session.UpdatedAt, session.NextCommentPollAt, body, now)
	}
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if cache != "" && cache != prev.cache {
		if err := upsertSessionCacheRow(ctx, q, session.Repo, session.IssueNumber, session.CachedIssueDetails.FetchedAt, cache); err != nil {
			return false, err
		}
	}
	return true, nil
}

type loadedSession struct {
	session Session
	row     sessionRow
}

func loadSessionRow(ctx context.Context, q dbExecer, repo string, issue int, withCache bool) (loadedSession, bool, error) {
	sessions, rows, err := querySessions(ctx, q, withCache, `s.repo = ? AND s.issue_number = ?`, repo, issue)
	if err != nil {
		return loadedSession{}, false, err
	}
	if len(sessions) == 0 {
		return loadedSession{}, false, nil
	}
	return loadedSession{session: sessions[0], row: rows[SessionKey(repo, issue)]}, true, nil
}

func splitSessionKey(key string) (string, int) {
	idx := strings.LastIndex(key, "#")
	if idx < 0 {
		return key, 0
	}
	var issue int
	_, _ = fmt.Sscanf(key[idx+1:], "%d", &issue)
	return key[:idx], issue
}

// LoadServiceConfig returns the service configuration, with defaults for
// anything not stored.
func (s *Store) LoadServiceConfig() (ServiceConfig, error) {
	conn, err := s.database()
	if err != nil {
		return ServiceConfig{}, err
	}
	config := defaultServiceConfig()
	var body string
	err = conn.QueryRowContext(context.Background(), `SELECT body FROM service_config WHERE id = 1`).Scan(&body)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ServiceConfig{}, err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(body), &config); err != nil {
			return ServiceConfig{}, fmt.Errorf("decode service config: %w", err)
		}
	}
	config.BlockedSessionInactivityTimeout = normalizeBlockedSessionInactivityTimeout(config.BlockedSessionInactivityTimeout)
	return config, nil
}

// SaveServiceConfig stores the service configuration.
func (s *Store) SaveServiceConfig(config ServiceConfig) error {
	conn, err := s.database()
	if err != nil {
		return err
	}
	return saveServiceConfigRow(context.Background(), conn, config)
}

func saveServiceConfigRow(ctx context.Context, q dbExecer, config ServiceConfig) error {
	config.BlockedSessionInactivityTimeout = normalizeBlockedSessionInactivityTimeout(config.BlockedSessionInactivityTimeout)
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO service_config (id, body, updated_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		string(body), time.Now().UTC().Format(time.RFC3339))
	return err
}

// LoadHardeningState returns the package hardening state for every PR.
func (s *Store) LoadHardeningState() (HardeningState, error) {
	conn, err := s.database()
	if err != nil {
		return nil, err
	}
	return loadHardeningRows(context.Background(), conn)
}

func loadHardeningRows(ctx context.Context, q dbExecer) (HardeningState, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, body FROM hardening_pr_state ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hs := make(HardeningState)
	for rows.Next() {
		var key, body string
		if err := rows.Scan(&key, &body); err != nil {
			return nil, err
		}
		var pr HardeningPRState
		if err := json.Unmarshal([]byte(body), &pr); err != nil {
			return nil, fmt.Errorf("decode hardening state %s: %w", key, err)
		}
		hs[key] = pr
	}
	return hs, rows.Err()
}

// SaveHardeningState replaces the stored package hardening state with hs.
func (s *Store) SaveHardeningState(hs HardeningState) error {
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
	if err := replaceHardeningRows(ctx, tx, hs); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceHardeningRows(ctx context.Context, q dbExecer, hs HardeningState) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM hardening_pr_state`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for key, pr := range hs {
		body, err := json.Marshal(pr)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO hardening_pr_state (key, repo, pr_number, body, updated_at) VALUES (?, ?, ?, ?, ?)`, key, pr.Repo, pr.PRNumber, string(body), now); err != nil {
			return err
		}
	}
	return nil
}

// LegacyExport is the pre-SQLite on-disk shape of Vigilante's state: the four
// JSON documents, with each session's issue cache inlined again.
type LegacyExport struct {
	WatchTargets []WatchTarget  `json:"watchlist"`
	Sessions     []Session      `json:"sessions"`
	Config       ServiceConfig  `json:"config"`
	Hardening    HardeningState `json:"hardening_state"`
}

// Export reads everything in the database back into the legacy JSON shapes.
func (s *Store) Export() (LegacyExport, error) {
	targets, err := s.LoadWatchTargets()
	if err != nil {
		return LegacyExport{}, err
	}
	sessions, err := s.ListSessionsWithIssueCache()
	if err != nil {
		return LegacyExport{}, err
	}
	config, err := s.LoadServiceConfig()
	if err != nil {
		return LegacyExport{}, err
	}
	hardening, err := s.LoadHardeningState()
	if err != nil {
		return LegacyExport{}, err
	}
	return LegacyExport{WatchTargets: targets, Sessions: sessions, Config: config, Hardening: hardening}, nil
}

// WriteExport writes the legacy files (watchlist.json, sessions.json,
// config.json, hardening_state.json) into dir.
func (s *Store) WriteExport(dir string) error {
	export, err := s.Export()
	if err != nil {
		return err
	}
	files := map[string]any{
		legacyWatchlistFileName: export.WatchTargets,
		legacySessionsFileName:  export.Sessions,
		legacyConfigFileName:    export.Config,
		legacyHardeningFileName: export.Hardening,
	}
	for name, value := range files {
		if err := writeJSONFile(filepath.Join(dir, name), value); err != nil {
			return err
		}
	}
	return nil
}
