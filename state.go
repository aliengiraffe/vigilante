package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type WatchTarget struct {
	Path          string `json:"path"`
	Repo          string `json:"repo"`
	Branch        string `json:"branch"`
	DaemonEnabled bool   `json:"daemon_enabled"`
	LastScanAt    string `json:"last_scan_at,omitempty"`
	AddedAt       string `json:"added_at,omitempty"`
}

type SessionStatus string

const (
	SessionStatusRunning SessionStatus = "running"
	SessionStatusSuccess SessionStatus = "success"
	SessionStatusFailed  SessionStatus = "failed"
)

type Session struct {
	RepoPath     string        `json:"repo_path"`
	Repo         string        `json:"repo"`
	IssueNumber  int           `json:"issue_number"`
	IssueTitle   string        `json:"issue_title,omitempty"`
	IssueURL     string        `json:"issue_url,omitempty"`
	Branch       string        `json:"branch"`
	WorktreePath string        `json:"worktree_path"`
	Status       SessionStatus `json:"status"`
	StartedAt    string        `json:"started_at,omitempty"`
	EndedAt      string        `json:"ended_at,omitempty"`
	UpdatedAt    string        `json:"updated_at,omitempty"`
	LastError    string        `json:"last_error,omitempty"`
}

type StateStore struct {
	root string
}

func NewStateStore() *StateStore {
	return &StateStore{root: discoverStateRoot()}
}

func (s *StateStore) Root() string {
	return s.root
}

func (s *StateStore) CodexHome() string {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(s.root, ".codex")
	}
	return filepath.Join(home, ".codex")
}

func (s *StateStore) EnsureLayout() error {
	for _, dir := range []string{s.root, s.LogsDir(), s.WorktreesDir()} {
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

func (s *StateStore) LogsDir() string {
	return filepath.Join(s.root, "logs")
}

func (s *StateStore) WorktreesDir() string {
	return filepath.Join(s.root, "worktrees")
}

func (s *StateStore) watchlistPath() string {
	return filepath.Join(s.root, "watchlist.json")
}

func (s *StateStore) sessionsPath() string {
	return filepath.Join(s.root, "sessions.json")
}

func (s *StateStore) LoadWatchTargets() ([]WatchTarget, error) {
	var targets []WatchTarget
	if err := readJSONFile(s.watchlistPath(), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func (s *StateStore) SaveWatchTargets(targets []WatchTarget) error {
	return writeJSONFile(s.watchlistPath(), targets)
}

func (s *StateStore) LoadSessions() ([]Session, error) {
	var sessions []Session
	if err := readJSONFile(s.sessionsPath(), &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *StateStore) SaveSessions(sessions []Session) error {
	return writeJSONFile(s.sessionsPath(), sessions)
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
