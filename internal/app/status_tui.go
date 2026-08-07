package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// Refresh cadences for the dashboard. Session and watch-target data comes from
// cheap local file reads, service state shells out to the OS service manager,
// and the rate-limit snapshot is itself a GitHub API call — so each source is
// polled on a cadence matched to its cost. The dashboard must never measurably
// consume the rate-limit budget it reports.
const (
	statusTUISessionsInterval  = time.Second
	statusTUIServiceInterval   = 5 * time.Second
	statusTUIRateLimitInterval = time.Minute
)

const (
	statusTUIGaugeWidth = 12
	// statusTUIFallbackWidth is the pane width used until the terminal
	// reports its size, or when it never does.
	statusTUIFallbackWidth = 80
	// statusTUIMinPaneWidth is the narrowest terminal that still gets
	// fixed-width bordered panes.
	statusTUIMinPaneWidth = 24
)

var (
	statusPaneStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	statusPaneTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	statusLabelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	statusDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	statusStrongStyle  = lipgloss.NewStyle().Bold(true)
	statusOKStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statusWarnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	statusErrStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	statusFooterStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	statusSessionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
)

// statusServiceSnapshot holds the service pane data for one refresh.
type statusServiceSnapshot struct {
	Info   serviceStatusInfo
	Err    error
	Loaded bool
}

// statusSessionsSnapshot holds the locally sourced watch-target and session
// data for one refresh.
type statusSessionsSnapshot struct {
	Repos  []watchedRepoStatus
	Groups []sessionGroup
	Count  int
	Loaded bool
}

// statusRateLimitSnapshot holds the GitHub rate-limit data for one refresh.
type statusRateLimitSnapshot struct {
	Snapshot  ghcli.RateLimitSnapshot
	Available bool
	Loaded    bool
}

type (
	statusServiceMsg       statusServiceSnapshot
	statusSessionsMsg      statusSessionsSnapshot
	statusRateLimitMsg     statusRateLimitSnapshot
	statusServiceTickMsg   time.Time
	statusSessionsTickMsg  time.Time
	statusRateLimitTickMsg time.Time
)

// statusModel is the Bubble Tea model behind `vigilante status`. It renders
// four panes — service, watched repositories, sessions, and GitHub rate
// limits — from the same data helpers the plain-text path uses.
type statusModel struct {
	loadService   func() statusServiceSnapshot
	loadSessions  func() statusSessionsSnapshot
	loadRateLimit func() statusRateLimitSnapshot

	serviceEvery   time.Duration
	sessionsEvery  time.Duration
	rateLimitEvery time.Duration

	service   statusServiceSnapshot
	sessions  statusSessionsSnapshot
	rateLimit statusRateLimitSnapshot

	width    int
	height   int
	offset   int
	quitting bool
}

// Init starts the initial load of every pane and arms the refresh tickers.
func (m statusModel) Init() tea.Cmd {
	return tea.Batch(
		m.serviceCmd(),
		m.sessionsCmd(),
		m.rateLimitCmd(),
		statusTick(m.serviceEvery, func(t time.Time) tea.Msg { return statusServiceTickMsg(t) }),
		statusTick(m.sessionsEvery, func(t time.Time) tea.Msg { return statusSessionsTickMsg(t) }),
		statusTick(m.rateLimitEvery, func(t time.Time) tea.Msg { return statusRateLimitTickMsg(t) }),
	)
}

// Update handles key presses, resizes, refresh ticks, and loaded data.
func (m statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			m.offset--
		case "down", "j":
			m.offset++
		case "pgup":
			m.offset -= m.pageSize()
		case "pgdown":
			m.offset += m.pageSize()
		case "home", "g":
			m.offset = 0
		case "end", "G":
			m.offset = len(m.contentLines())
		}
		m.clampOffset()
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampOffset()
		return m, nil
	case statusServiceMsg:
		m.service = statusServiceSnapshot(msg)
		m.service.Loaded = true
		m.clampOffset()
		return m, nil
	case statusSessionsMsg:
		m.sessions = statusSessionsSnapshot(msg)
		m.sessions.Loaded = true
		m.clampOffset()
		return m, nil
	case statusRateLimitMsg:
		m.rateLimit = statusRateLimitSnapshot(msg)
		m.rateLimit.Loaded = true
		m.clampOffset()
		return m, nil
	case statusServiceTickMsg:
		return m, tea.Batch(m.serviceCmd(), statusTick(m.serviceEvery, func(t time.Time) tea.Msg { return statusServiceTickMsg(t) }))
	case statusSessionsTickMsg:
		return m, tea.Batch(m.sessionsCmd(), statusTick(m.sessionsEvery, func(t time.Time) tea.Msg { return statusSessionsTickMsg(t) }))
	case statusRateLimitTickMsg:
		return m, tea.Batch(m.rateLimitCmd(), statusTick(m.rateLimitEvery, func(t time.Time) tea.Msg { return statusRateLimitTickMsg(t) }))
	}
	return m, nil
}

// View renders the visible window of the dashboard plus the key hints.
func (m statusModel) View() string {
	if m.quitting {
		return ""
	}
	lines := m.contentLines()
	footer := m.footer()
	if m.height <= 0 {
		return strings.Join(append(lines, footer), "\n")
	}
	visible := m.height - 1
	if visible <= 0 {
		return footer
	}
	offset := m.offset
	if max := len(lines) - visible; offset > max {
		offset = max
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + visible
	if end > len(lines) {
		end = len(lines)
	}
	window := append([]string{}, lines[offset:end]...)
	return strings.Join(append(window, footer), "\n")
}

func (m statusModel) serviceCmd() tea.Cmd {
	if m.loadService == nil {
		return nil
	}
	return func() tea.Msg { return statusServiceMsg(m.loadService()) }
}

func (m statusModel) sessionsCmd() tea.Cmd {
	if m.loadSessions == nil {
		return nil
	}
	return func() tea.Msg { return statusSessionsMsg(m.loadSessions()) }
}

func (m statusModel) rateLimitCmd() tea.Cmd {
	if m.loadRateLimit == nil {
		return nil
	}
	return func() tea.Msg { return statusRateLimitMsg(m.loadRateLimit()) }
}

func statusTick(every time.Duration, wrap func(time.Time) tea.Msg) tea.Cmd {
	if every <= 0 {
		return nil
	}
	return tea.Tick(every, wrap)
}

func (m statusModel) pageSize() int {
	if m.height > 2 {
		return m.height - 2
	}
	return 1
}

func (m *statusModel) clampOffset() {
	max := len(m.contentLines()) - 1
	if m.height > 1 {
		max = len(m.contentLines()) - (m.height - 1)
	}
	if max < 0 {
		max = 0
	}
	if m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m statusModel) contentLines() []string {
	panes := []string{
		m.renderPane("Service", m.serviceLines()),
		m.renderPane(fmt.Sprintf("Watched repositories (%d)", len(m.sessions.Repos)), m.repoLines()),
		m.renderPane(fmt.Sprintf("Sessions (%d)", m.sessions.Count), m.sessionLines()),
		m.renderPane("GitHub rate limits", m.rateLimitLines()),
	}
	return strings.Split(strings.Join(panes, "\n"), "\n")
}

func (m statusModel) renderPane(title string, body []string) string {
	if len(body) == 0 {
		body = []string{statusDimStyle.Render("loading…")}
	}
	content := statusPaneTitle.Render(title) + "\n" + strings.Join(body, "\n")
	style := statusPaneStyle
	if inner := m.innerWidth(); inner > 0 {
		style = style.Width(inner)
	}
	if m.width > 0 {
		style = style.MaxWidth(m.width)
	}
	return style.Render(content)
}

// innerWidth is the pane content width. Terminals that never report a size
// fall back to a conventional 80 columns, and terminals too narrow for a
// bordered pane get 0 so panes size themselves instead of overflowing.
func (m statusModel) innerWidth() int {
	if m.width <= 0 {
		return statusTUIFallbackWidth - 2
	}
	if m.width < statusTUIMinPaneWidth {
		return 0
	}
	return m.width - 2
}

func (m statusModel) footer() string {
	return statusFooterStyle.Render("q/esc quit · ↑/↓ scroll · sessions 1s · service 5s · rate limits 60s")
}

func (m statusModel) serviceLines() []string {
	if !m.service.Loaded {
		return nil
	}
	if m.service.Err != nil {
		return []string{statusErrStyle.Render(fmt.Sprintf("unavailable: %s", m.service.Err))}
	}
	s := m.service.Info
	lines := []string{
		statusField("state", statusStateStyle(s.State).Render(valueOrUnknown(s.State))),
		statusField("manager", valueOrUnknown(s.Manager)),
		statusField("service", valueOrUnknown(s.Service)),
		statusField("path", valueOrUnknown(s.FilePath)),
		statusField("installed", statusBool(s.Installed)),
		statusField("running", statusBool(s.Running)),
	}
	if s.Installed {
		lines = append(lines, statusField("daemon version", statusDaemonVersion(s)))
	}
	return lines
}

func statusDaemonVersion(s serviceStatusInfo) string {
	switch {
	case s.DaemonVersion != "" && s.Running:
		return s.DaemonVersion
	case s.DaemonVersion != "":
		return fmt.Sprintf("%s (configured binary; service not running)", s.DaemonVersion)
	case s.Running:
		return statusDimStyle.Render("unavailable")
	default:
		return statusDimStyle.Render("unavailable (service not running)")
	}
}

func statusStateStyle(serviceState string) lipgloss.Style {
	switch serviceState {
	case "running":
		return statusOKStyle
	case "stopped":
		return statusWarnStyle
	default:
		return statusErrStyle
	}
}

func statusBool(v bool) string {
	if v {
		return statusOKStyle.Render("yes")
	}
	return statusDimStyle.Render("no")
}

func statusField(label, value string) string {
	return fmt.Sprintf("%s %s", statusLabelStyle.Render(label+":"), value)
}

func (m statusModel) repoLines() []string {
	if !m.sessions.Loaded {
		return nil
	}
	if len(m.sessions.Repos) == 0 {
		return []string{statusDimStyle.Render("none configured")}
	}
	lines := make([]string, 0, len(m.sessions.Repos)*2)
	for _, status := range m.sessions.Repos {
		target := status.Target
		activity := formatWatchActivity(status)
		marker := statusDimStyle.Render("○")
		if status.ActiveCount > 0 {
			marker = statusOKStyle.Render("●")
		} else if status.BlockedCount > 0 {
			marker = statusWarnStyle.Render("●")
		}
		lines = append(lines, fmt.Sprintf("%s %s  %s", marker, statusStrongStyle.Render(valueOrUnknown(target.Repo)), statusLabelStyle.Render(activity)))
		details := []string{
			fmt.Sprintf("branch %s (%s)", valueOrUnknown(target.Branch), target.EffectiveBranchMode()),
			fmt.Sprintf("provider %s", valueOrUnknown(target.Provider)),
		}
		if issueBackend := strings.TrimSpace(target.EffectiveIssueBackend()); issueBackend != "" && issueBackend != "github" {
			details = append(details, fmt.Sprintf("issues %s", issueBackend))
		}
		if scan := formatLastScan(target.LastScanAt); scan != "" {
			details = append(details, scan)
		}
		lines = append(lines, "  "+statusDimStyle.Render(strings.Join(details, " · ")))
	}
	return lines
}

func (m statusModel) sessionLines() []string {
	if !m.sessions.Loaded {
		return nil
	}
	if len(m.sessions.Groups) == 0 {
		return []string{statusDimStyle.Render("no active sessions")}
	}
	var lines []string
	for i, g := range m.sessions.Groups {
		if i > 0 {
			lines = append(lines, "")
		}
		total := len(g.Sessions) + g.CompletedCount + g.FailedCount
		lines = append(lines, statusStrongStyle.Render(fmt.Sprintf("%s (%d)", g.Label, total)))
		if g.CompletedCount > 0 || g.FailedCount > 0 {
			lines = append(lines,
				"  "+statusOKStyle.Render(fmt.Sprintf("completed: %d", g.CompletedCount)),
				"  "+statusErrStyle.Render(fmt.Sprintf("failed: %d", g.FailedCount)),
			)
			continue
		}
		for _, s := range g.Sessions {
			lines = append(lines, statusSessionStyle.Render(formatSessionRow(s)))
		}
	}
	return lines
}

func (m statusModel) rateLimitLines() []string {
	if !m.rateLimit.Loaded {
		return nil
	}
	if !m.rateLimit.Available {
		return []string{statusDimStyle.Render("unavailable")}
	}
	snapshot := m.rateLimit.Snapshot
	lines := make([]string, 0, 3)
	for _, resource := range []struct {
		label string
		value ghcli.RateLimitResource
	}{
		{"core", snapshot.Core},
		{"graphql", snapshot.GraphQL},
		{"search", snapshot.Search},
	} {
		if resource.value.Limit == 0 {
			continue
		}
		lines = append(lines, formatRateLimitGaugeRow(resource.label, resource.value))
	}
	if len(lines) == 0 {
		return []string{statusDimStyle.Render("unavailable")}
	}
	return lines
}

func formatRateLimitGaugeRow(label string, r ghcli.RateLimitResource) string {
	filled := 0
	if r.Limit > 0 {
		filled = r.Remaining * statusTUIGaugeWidth / r.Limit
	}
	if filled < 0 {
		filled = 0
	}
	if filled > statusTUIGaugeWidth {
		filled = statusTUIGaugeWidth
	}
	gauge := statusRateLimitStyle(r).Render(strings.Repeat("█", filled)) + statusDimStyle.Render(strings.Repeat("░", statusTUIGaugeWidth-filled))
	return fmt.Sprintf("%s %s %s",
		statusLabelStyle.Render(fmt.Sprintf("%-8s", label)),
		gauge,
		fmt.Sprintf("%d/%d, resets %s", r.Remaining, r.Limit, formatRateLimitReset(r)),
	)
}

func statusRateLimitStyle(r ghcli.RateLimitResource) lipgloss.Style {
	if r.Limit <= 0 {
		return statusDimStyle
	}
	switch ratio := float64(r.Remaining) / float64(r.Limit); {
	case ratio > 0.5:
		return statusOKStyle
	case ratio > 0.2:
		return statusWarnStyle
	default:
		return statusErrStyle
	}
}

// newStatusModel builds the dashboard model bound to this App's data helpers.
func (a *App) newStatusModel(ctx context.Context) statusModel {
	return statusModel{
		loadService:    func() statusServiceSnapshot { return a.loadStatusService(ctx) },
		loadSessions:   func() statusSessionsSnapshot { return a.loadStatusSessions(ctx) },
		loadRateLimit:  func() statusRateLimitSnapshot { return a.loadStatusRateLimit(ctx) },
		serviceEvery:   statusTUIServiceInterval,
		sessionsEvery:  statusTUISessionsInterval,
		rateLimitEvery: statusTUIRateLimitInterval,
	}
}

func (a *App) loadStatusService(ctx context.Context) statusServiceSnapshot {
	info, err := a.statusServiceSection(ctx)
	if err != nil {
		return statusServiceSnapshot{Err: err, Loaded: true}
	}
	return statusServiceSnapshot{Info: info, Loaded: true}
}

func (a *App) loadStatusSessions(_ context.Context) statusSessionsSnapshot {
	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		targets = nil
	}
	sessions, err := a.state.LoadSessions()
	if err != nil {
		sessions = nil
	}
	inactivityTimeout := state.DefaultBlockedSessionInactivityTimeout
	if cfg, cfgErr := a.state.LoadServiceConfig(); cfgErr == nil {
		if parsed, parseErr := time.ParseDuration(cfg.BlockedSessionInactivityTimeout); parseErr == nil && parsed > 0 {
			inactivityTimeout = parsed
		}
	}
	visible := visibleStatusSessions(sessions)
	return statusSessionsSnapshot{
		Repos:  watchedRepoStatuses(targets, sessions),
		Groups: groupSessions(visible, a.clock(), inactivityTimeout),
		Count:  len(visible),
		Loaded: true,
	}
}

func (a *App) loadStatusRateLimit(ctx context.Context) statusRateLimitSnapshot {
	if a.rateLimiter == nil {
		return statusRateLimitSnapshot{Loaded: true}
	}
	snapshot, err := a.rateLimiter.GetRateLimitSnapshot(ctx)
	if err != nil {
		return statusRateLimitSnapshot{Loaded: true}
	}
	return statusRateLimitSnapshot{Snapshot: snapshot, Available: true, Loaded: true}
}

// statusDashboard runs the live Bubble Tea dashboard until the user quits or
// the context is cancelled. Bubble Tea restores the terminal on exit, error
// included.
func (a *App) statusDashboard(ctx context.Context) error {
	program := tea.NewProgram(
		a.newStatusModel(ctx),
		tea.WithContext(ctx),
		tea.WithInput(a.stdin),
		tea.WithOutput(a.stdout),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// statusUsesDashboard reports whether the live dashboard should render. An
// explicit --plain always wins, and a non-terminal stdout falls back to plain
// text so piped and redirected output stays script-parseable.
func (a *App) statusUsesDashboard(plain bool) bool {
	return !plain && a.stdoutIsTerminal()
}

// stdoutIsTerminal reports whether stdout is an interactive character device.
// Piped, redirected, and test writers are not, so they take the plain path.
func (a *App) stdoutIsTerminal() bool {
	file, ok := a.stdout.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}
