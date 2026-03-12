package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nicobistolfi/vigilante/internal/logtime"
)

type WatchTarget struct {
	Path          string   `json:"path"`
	Repo          string   `json:"repo"`
	Branch        string   `json:"branch"`
	Provider      string   `json:"provider,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Assignee      string   `json:"assignee,omitempty"`
	MaxParallel   int      `json:"max_parallel_sessions"`
	DaemonEnabled bool     `json:"daemon_enabled"`
	LastScanAt    string   `json:"last_scan_at,omitempty"`
	AddedAt       string   `json:"added_at,omitempty"`
}

const DefaultMaxParallelSessions = 1

type SessionStatus string

const (
	SessionStatusRunning  SessionStatus = "running"
	SessionStatusBlocked  SessionStatus = "blocked"
	SessionStatusResuming SessionStatus = "resuming"
	SessionStatusSuccess  SessionStatus = "success"
	SessionStatusFailed   SessionStatus = "failed"
)

type BlockedReason struct {
	Kind      string `json:"kind,omitempty"`
	Operation string `json:"operation,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type Session struct {
	RepoPath             string        `json:"repo_path"`
	Repo                 string        `json:"repo"`
	Provider             string        `json:"provider,omitempty"`
	IssueNumber          int           `json:"issue_number"`
	IssueTitle           string        `json:"issue_title,omitempty"`
	IssueURL             string        `json:"issue_url,omitempty"`
	Branch               string        `json:"branch"`
	WorktreePath         string        `json:"worktree_path"`
	Status               SessionStatus `json:"status"`
	PullRequestNumber    int           `json:"pull_request_number,omitempty"`
	PullRequestURL       string        `json:"pull_request_url,omitempty"`
	PullRequestState     string        `json:"pull_request_state,omitempty"`
	PullRequestMergedAt  string        `json:"pull_request_merged_at,omitempty"`
	LastMaintainedAt     string        `json:"last_maintained_at,omitempty"`
	LastMaintenanceError string        `json:"last_maintenance_error,omitempty"`
	BlockedAt            string        `json:"blocked_at,omitempty"`
	BlockedStage         string        `json:"blocked_stage,omitempty"`
	BlockedReason        BlockedReason `json:"blocked_reason,omitempty"`
	RetryPolicy          string        `json:"retry_policy,omitempty"`
	ResumeRequired       bool          `json:"resume_required,omitempty"`
	ResumeHint           string        `json:"resume_hint,omitempty"`
	LastResumeSource     string        `json:"last_resume_source,omitempty"`
	LastResumeCommentID  int64         `json:"last_resume_comment_id,omitempty"`
	LastResumeCommentAt  string        `json:"last_resume_comment_at,omitempty"`
	LastCleanupSource    string        `json:"last_cleanup_source,omitempty"`
	LastCleanupCommentID int64         `json:"last_cleanup_comment_id,omitempty"`
	LastCleanupCommentAt string        `json:"last_cleanup_comment_at,omitempty"`
	RecoveredAt          string        `json:"recovered_at,omitempty"`
	MonitoringStoppedAt  string        `json:"monitoring_stopped_at,omitempty"`
	CleanupCompletedAt   string        `json:"cleanup_completed_at,omitempty"`
	CleanupError         string        `json:"cleanup_error,omitempty"`
	ProcessID            int           `json:"process_id,omitempty"`
	StartedAt            string        `json:"started_at,omitempty"`
	LastHeartbeatAt      string        `json:"last_heartbeat_at,omitempty"`
	EndedAt              string        `json:"ended_at,omitempty"`
	UpdatedAt            string        `json:"updated_at,omitempty"`
	LastError            string        `json:"last_error,omitempty"`
}

type Store struct {
	root string
}

func NewStore() *Store {
	return &Store{root: discoverStateRoot()}
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) CodexHome() string {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(s.root, ".codex")
	}
	return filepath.Join(home, ".codex")
}

func (s *Store) EnsureLayout() error {
	for _, dir := range []string{s.root, s.LogsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, path := range []string{s.watchlistPath(), s.sessionsPath()} {
		if err := ensureJSONArrayFile(path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LogsDir() string {
	return filepath.Join(s.root, "logs")
}

func (s *Store) DaemonLogPath() string {
	return filepath.Join(s.LogsDir(), "vigilante.log")
}

func (s *Store) SessionLogPath(issueNumber int) string {
	return filepath.Join(s.LogsDir(), fmt.Sprintf("issue-%d.log", issueNumber))
}

func (s *Store) watchlistPath() string {
	return filepath.Join(s.root, "watchlist.json")
}

func (s *Store) sessionsPath() string {
	return filepath.Join(s.root, "sessions.json")
}

func (s *Store) scanLockPath() string {
	return filepath.Join(s.root, "scan.lock")
}

func (s *Store) LoadWatchTargets() ([]WatchTarget, error) {
	var targets []WatchTarget
	if err := readJSONFile(s.watchlistPath(), &targets); err != nil {
		return nil, err
	}
	for i := range targets {
		if strings.TrimSpace(targets[i].Provider) == "" {
			targets[i].Provider = "codex"
		}
		targets[i].MaxParallel = normalizeMaxParallelSessions(targets[i].MaxParallel)
	}
	return targets, nil
}

func (s *Store) SaveWatchTargets(targets []WatchTarget) error {
	for i := range targets {
		targets[i].MaxParallel = normalizeMaxParallelSessions(targets[i].MaxParallel)
	}
	return writeJSONFile(s.watchlistPath(), targets)
}

func (s *Store) LoadSessions() ([]Session, error) {
	var sessions []Session
	if err := readJSONFile(s.sessionsPath(), &sessions); err != nil {
		return nil, err
	}
	for i := range sessions {
		if strings.TrimSpace(sessions[i].Provider) == "" {
			sessions[i].Provider = "codex"
		}
	}
	return sessions, nil
}

func (s *Store) SaveSessions(sessions []Session) error {
	return writeJSONFile(s.sessionsPath(), sessions)
}

func normalizeMaxParallelSessions(value int) int {
	if value < 1 {
		return DefaultMaxParallelSessions
	}
	return value
}

func discoverStateRoot() string {
	if value := os.Getenv("VIGILANTE_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".vigilante"
	}
	return filepath.Join(home, ".vigilante")
}

func ensureJSONArrayFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte("[]\n"), 0o644)
}

func readJSONFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func (s *Store) AppendDaemonLog(format string, args ...any) {
	appendLogFile(s.DaemonLogPath(), fmt.Sprintf(format, args...))
}

func appendLogFile(path string, message string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "[%s] %s\n", logtime.FormatLocal(time.Now()), strings.TrimSpace(message))
}

func (s *Store) TryWithScanLock(fn func() error) (bool, error) {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(s.scanLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()

	return true, fn()
}
