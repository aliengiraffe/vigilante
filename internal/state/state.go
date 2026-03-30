package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/logtime"
	"github.com/nicobistolfi/vigilante/internal/repo"
)

type WatchTarget struct {
	Path           string              `json:"path"`
	Repo           string              `json:"repo"`
	BranchMode     BranchMode          `json:"branch_mode,omitempty"`
	Branch         string              `json:"branch"`
	Classification repo.Classification `json:"classification,omitempty"`
	Provider       string              `json:"provider,omitempty"`
	Labels         []string            `json:"labels,omitempty"`
	Assignee       string              `json:"assignee,omitempty"`
	MaxParallel    int                 `json:"max_parallel_sessions"`
	LastScanAt     string              `json:"last_scan_at,omitempty"`
	AddedAt        string              `json:"added_at,omitempty"`
	IssueBackend   string              `json:"issue_backend,omitempty"`
	IssueStage     string              `json:"issue_tracker_stage,omitempty"`
	GitBackend     string              `json:"git_backend,omitempty"`
	PRBackend      string              `json:"pr_backend,omitempty"`
	ProjectRef     string              `json:"project_ref,omitempty"`
}

type BranchMode string

const (
	BranchModeAuto   BranchMode = "auto"
	BranchModePinned BranchMode = "pinned"
)

// EffectiveIssueBackend returns the issue-tracking backend for this target.
// Defaults to "github" when not explicitly configured.
func (t WatchTarget) EffectiveIssueBackend() string {
	if b := strings.TrimSpace(t.IssueBackend); b != "" {
		return b
	}
	return "github"
}

// EffectiveIssueStage returns the configured issue-tracker stage.
// Linear-backed targets default to "Todo" when the field is not set.
func (t WatchTarget) EffectiveIssueStage() string {
	stage := strings.TrimSpace(t.IssueStage)
	if stage != "" {
		return stage
	}
	if t.EffectiveIssueBackend() == "linear" {
		return "Todo"
	}
	return ""
}

// EffectiveGitBackend returns the git-hosting backend for this target.
// Defaults to "github" when not explicitly configured.
func (t WatchTarget) EffectiveGitBackend() string {
	if b := strings.TrimSpace(t.GitBackend); b != "" {
		return b
	}
	return "github"
}

// EffectivePRBackend returns the pull-request backend for this target.
// Defaults to "github" when not explicitly configured.
func (t WatchTarget) EffectivePRBackend() string {
	if b := strings.TrimSpace(t.PRBackend); b != "" {
		return b
	}
	return "github"
}

// EffectiveProjectRef returns the backend-specific project reference.
// For GitHub targets this falls back to the Repo slug.
func (t WatchTarget) EffectiveProjectRef() string {
	if ref := strings.TrimSpace(t.ProjectRef); ref != "" {
		return ref
	}
	return t.Repo
}

func (t WatchTarget) EffectiveBranchMode() BranchMode {
	switch t.BranchMode {
	case BranchModeAuto:
		return BranchModeAuto
	default:
		return BranchModePinned
	}
}

const DefaultMaxParallelSessions = 0
const DefaultBlockedSessionInactivityTimeout = 20 * time.Minute

type ServiceConfig struct {
	BlockedSessionInactivityTimeout string `json:"blocked_session_inactivity_timeout,omitempty"`
}

type SessionStatus string

const (
	SessionStatusRunning  SessionStatus = "running"
	SessionStatusBlocked  SessionStatus = "blocked"
	SessionStatusResuming SessionStatus = "resuming"
	SessionStatusSuccess  SessionStatus = "success"
	SessionStatusFailed   SessionStatus = "failed"
	SessionStatusClosed   SessionStatus = "closed"
)

type BlockedReason struct {
	Kind      string `json:"kind,omitempty"`
	Operation string `json:"operation,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type Session struct {
	RepoPath                       string        `json:"repo_path"`
	Repo                           string        `json:"repo"`
	Provider                       string        `json:"provider,omitempty"`
	IssueBackend                   string        `json:"issue_backend,omitempty"`
	GitBackend                     string        `json:"git_backend,omitempty"`
	PRBackend                      string        `json:"pr_backend,omitempty"`
	IssueNumber                    int           `json:"issue_number"`
	IssueTitle                     string        `json:"issue_title,omitempty"`
	IssueBody                      string        `json:"issue_body,omitempty"`
	IssueURL                       string        `json:"issue_url,omitempty"`
	BaseBranch                     string        `json:"base_branch,omitempty"`
	Branch                         string        `json:"branch"`
	WorktreePath                   string        `json:"worktree_path"`
	ReusedRemoteBranch             string        `json:"reused_remote_branch,omitempty"`
	BranchDiffSummary              string        `json:"branch_diff_summary,omitempty"`
	Status                         SessionStatus `json:"status"`
	PullRequestNumber              int           `json:"pull_request_number,omitempty"`
	PullRequestURL                 string        `json:"pull_request_url,omitempty"`
	PullRequestState               string        `json:"pull_request_state,omitempty"`
	PullRequestMergedAt            string        `json:"pull_request_merged_at,omitempty"`
	PullRequestHeadBranch          string        `json:"pull_request_head_branch,omitempty"`
	PullRequestBaseBranch          string        `json:"pull_request_base_branch,omitempty"`
	PullRequestMergeable           string        `json:"pull_request_mergeable,omitempty"`
	PullRequestMergeStateStatus    string        `json:"pull_request_merge_state_status,omitempty"`
	PullRequestReviewDecision      string        `json:"pull_request_review_decision,omitempty"`
	PullRequestChecksState         string        `json:"pull_request_checks_state,omitempty"`
	PullRequestStatusFingerprint   string        `json:"pull_request_status_fingerprint,omitempty"`
	LastMaintainedAt               string        `json:"last_maintained_at,omitempty"`
	LastMaintenanceError           string        `json:"last_maintenance_error,omitempty"`
	LastCIRemediationFingerprint   string        `json:"last_ci_remediation_fingerprint,omitempty"`
	LastCIRemediationAttemptedAt   string        `json:"last_ci_remediation_attempted_at,omitempty"`
	BlockedAt                      string        `json:"blocked_at,omitempty"`
	BlockedStage                   string        `json:"blocked_stage,omitempty"`
	BlockedReason                  BlockedReason `json:"blocked_reason,omitempty"`
	RetryPolicy                    string        `json:"retry_policy,omitempty"`
	ResumeAfter                    string        `json:"resume_after,omitempty"`
	ResumeRequired                 bool          `json:"resume_required,omitempty"`
	ResumeHint                     string        `json:"resume_hint,omitempty"`
	LastResumeSource               string        `json:"last_resume_source,omitempty"`
	LastResumeCommentID            int64         `json:"last_resume_comment_id,omitempty"`
	LastResumeCommentAt            string        `json:"last_resume_comment_at,omitempty"`
	LastResumeFailureFingerprint   string        `json:"last_resume_failure_fingerprint,omitempty"`
	LastResumeFailureCommentedAt   string        `json:"last_resume_failure_commented_at,omitempty"`
	LastDispatchFailureFingerprint string        `json:"last_dispatch_failure_fingerprint,omitempty"`
	LastDispatchFailureCommentedAt string        `json:"last_dispatch_failure_commented_at,omitempty"`
	LastGitHubDelayResetAt         string        `json:"last_github_delay_reset_at,omitempty"`
	LastGitHubDelayCommentedAt     string        `json:"last_github_delay_commented_at,omitempty"`
	LastIterationCommentID         int64         `json:"last_iteration_comment_id,omitempty"`
	LastIterationCommentAt         string        `json:"last_iteration_comment_at,omitempty"`
	IterationPromptContext         string        `json:"iteration_prompt_context,omitempty"`
	IterationInProgress            bool          `json:"iteration_in_progress,omitempty"`
	LastCleanupSource              string        `json:"last_cleanup_source,omitempty"`
	LastCleanupCommentID           int64         `json:"last_cleanup_comment_id,omitempty"`
	LastCleanupCommentAt           string        `json:"last_cleanup_comment_at,omitempty"`
	LastRecreateSource             string        `json:"last_recreate_source,omitempty"`
	LastRecreateCommentID          int64         `json:"last_recreate_comment_id,omitempty"`
	LastRecreateCommentAt          string        `json:"last_recreate_comment_at,omitempty"`
	RecreatedAsIssue               int           `json:"recreated_as_issue,omitempty"`
	RecreatedAsIssueURL            string        `json:"recreated_as_issue_url,omitempty"`
	StaleAutoRestartAttempts       int           `json:"stale_auto_restart_attempts,omitempty"`
	StaleAutoRestartPendingSince   string        `json:"stale_auto_restart_pending_since,omitempty"`
	StaleAutoRestartStoppedAt      string        `json:"stale_auto_restart_stopped_at,omitempty"`
	RecoveredAt                    string        `json:"recovered_at,omitempty"`
	MonitoringStoppedAt            string        `json:"monitoring_stopped_at,omitempty"`
	CleanupCompletedAt             string        `json:"cleanup_completed_at,omitempty"`
	CleanupError                   string        `json:"cleanup_error,omitempty"`
	ProcessID                      int           `json:"process_id,omitempty"`
	StartedAt                      string        `json:"started_at,omitempty"`
	LastHeartbeatAt                string        `json:"last_heartbeat_at,omitempty"`
	EndedAt                        string        `json:"ended_at,omitempty"`
	UpdatedAt                      string        `json:"updated_at,omitempty"`
	LastError                      string        `json:"last_error,omitempty"`
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

func (s *Store) ClaudeHome() string {
	if value := os.Getenv("CLAUDE_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(s.root, ".claude")
	}
	return filepath.Join(home, ".claude")
}

func (s *Store) GeminiHome() string {
	if value := os.Getenv("GEMINI_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(s.root, ".gemini")
	}
	return filepath.Join(home, ".gemini")
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
	if err := ensureJSONFile(s.serviceConfigPath(), defaultServiceConfig()); err != nil {
		return err
	}
	return nil
}

func (s *Store) LogsDir() string {
	return filepath.Join(s.root, "logs")
}

func (s *Store) DaemonLogPath() string {
	return filepath.Join(s.LogsDir(), "vigilante.log")
}

func (s *Store) AccessLogPath() string {
	return filepath.Join(s.LogsDir(), "access.jsonl")
}

var invalidSessionLogNameChars = regexp.MustCompile(`[^A-Za-z0-9]+`)

func (s *Store) SessionLogPath(repoSlug string, issueNumber int) string {
	return filepath.Join(s.LogsDir(), fmt.Sprintf("%s-issue-%d.log", sanitizeSessionLogRepoSlug(repoSlug), issueNumber))
}

func sanitizeSessionLogRepoSlug(repoSlug string) string {
	parts := strings.Split(strings.TrimSpace(repoSlug), "/")
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = invalidSessionLogNameChars.ReplaceAllString(strings.TrimSpace(part), "-")
		part = strings.Trim(part, "-")
		if part == "" {
			continue
		}
		sanitized = append(sanitized, part)
	}
	if len(sanitized) == 0 {
		return "unknown-repo"
	}
	return strings.Join(sanitized, "-")
}

func (s *Store) watchlistPath() string {
	return filepath.Join(s.root, "watchlist.json")
}

func (s *Store) sessionsPath() string {
	return filepath.Join(s.root, "sessions.json")
}

func (s *Store) serviceConfigPath() string {
	return filepath.Join(s.root, "config.json")
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

func (s *Store) LoadServiceConfig() (ServiceConfig, error) {
	config := defaultServiceConfig()
	if err := readJSONFile(s.serviceConfigPath(), &config); err != nil {
		return ServiceConfig{}, err
	}
	config.BlockedSessionInactivityTimeout = normalizeBlockedSessionInactivityTimeout(config.BlockedSessionInactivityTimeout)
	return config, nil
}

func (s *Store) SaveServiceConfig(config ServiceConfig) error {
	config.BlockedSessionInactivityTimeout = normalizeBlockedSessionInactivityTimeout(config.BlockedSessionInactivityTimeout)
	return writeJSONFile(s.serviceConfigPath(), config)
}

func normalizeMaxParallelSessions(value int) int {
	if value < 0 {
		return 1
	}
	if value == 0 {
		return DefaultMaxParallelSessions
	}
	return value
}

func normalizeBlockedSessionInactivityTimeout(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultBlockedSessionInactivityTimeout.String()
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return DefaultBlockedSessionInactivityTimeout.String()
	}
	return parsed.String()
}

func defaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		BlockedSessionInactivityTimeout: DefaultBlockedSessionInactivityTimeout.String(),
	}
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

func ensureJSONFile(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSONFile(path, value)
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

func (s *Store) AppendAccessLogEntry(value environment.AccessLogEntry) {
	appendJSONLine(s.AccessLogPath(), value)
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

func appendJSONLine(path string, value any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
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
