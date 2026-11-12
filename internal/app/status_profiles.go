package app

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/state"
)

// agentProfileReasonLimit caps the validation reason shown for an unusable
// profile in the status summary.
const agentProfileReasonLimit = 80

// agentProfileDefaultName labels watch targets that run without a profile.
const agentProfileDefaultName = "default"

// agentProfileSource records how a watch target's profile was chosen.
type agentProfileSource string

const (
	agentProfileSourceDefault  agentProfileSource = "default"
	agentProfileSourceExplicit agentProfileSource = "explicit"
	// agentProfileSourceAuto is reserved for profiles chosen by regex
	// auto-selection (#552).
	agentProfileSourceAuto agentProfileSource = "auto"
)

// watchTargetProfile is the effective agent profile of one watch target as
// shown by `vigilante status`.
type watchTargetProfile struct {
	Name   string
	Source agentProfileSource
	// Err is set when the profile is undefined or invalid, so sessions for
	// the target will be blocked.
	Err error
}

// Unavailable reports whether the target's sessions will be blocked because
// its profile cannot be used.
func (p watchTargetProfile) Unavailable() bool {
	return p.Source != agentProfileSourceDefault && p.Err != nil
}

// agentProfileSummaryRow is one line of the Agent profiles summary.
type agentProfileSummaryRow struct {
	Name  string
	Auto  bool
	Repos []string
	// Problem is "INVALID: <reason>" or "UNDEFINED" for unusable profiles.
	Problem string
}

// agentProfilesView is the profile information shared by the plain and TUI
// status renderers.
type agentProfilesView struct {
	// Enabled is true when at least one profile is defined or referenced.
	// When false, status output omits every profile field and the summary.
	Enabled bool
	// ValidityUnknown is true when the service config could not be loaded,
	// so explicit profile names are shown without validation.
	ValidityUnknown bool
	Rows            []agentProfileSummaryRow
}

// resolveWatchTargetProfile returns the effective profile for target. This is
// the single call site to swap for #552's effective-profile resolver.
func resolveWatchTargetProfile(target state.WatchTarget, config state.ServiceConfig, configErr error) watchTargetProfile {
	if target.AgentProfile == "" {
		return watchTargetProfile{Name: agentProfileDefaultName, Source: agentProfileSourceDefault}
	}
	profile := watchTargetProfile{Name: target.AgentProfile, Source: agentProfileSourceExplicit}
	if configErr == nil {
		if _, err := config.AgentProfile(target.AgentProfile); err != nil {
			profile.Err = err
		}
	}
	return profile
}

// applyAgentProfiles fills in each status's profile and builds the summary.
// When no profile is defined or referenced it leaves statuses untouched and
// returns a disabled view so status output is unchanged for users who do not
// use profiles.
func applyAgentProfiles(statuses []watchedRepoStatus, config state.ServiceConfig, configErr error) agentProfilesView {
	var defined map[string]state.AgentProfile
	if configErr == nil {
		defined = config.AgentProfiles
	}
	profiles := make([]watchTargetProfile, len(statuses))
	enabled := len(defined) > 0
	for i, status := range statuses {
		profiles[i] = resolveWatchTargetProfile(status.Target, config, configErr)
		if profiles[i].Source != agentProfileSourceDefault {
			enabled = true
		}
	}
	if !enabled {
		return agentProfilesView{}
	}

	view := agentProfilesView{Enabled: true, ValidityUnknown: configErr != nil}
	repos := make(map[string][]string)
	auto := make(map[string]bool)
	var defaultRepos []string
	for i := range statuses {
		profile := profiles[i]
		statuses[i].Profile = &profiles[i]
		repo := valueOrUnknown(statuses[i].Target.Repo)
		if profile.Source == agentProfileSourceDefault {
			defaultRepos = appendUnique(defaultRepos, repo)
			continue
		}
		repos[profile.Name] = appendUnique(repos[profile.Name], repo)
		if profile.Source == agentProfileSourceAuto {
			auto[profile.Name] = true
		}
	}

	names := make([]string, 0, len(defined)+len(repos))
	seen := make(map[string]bool)
	for name := range defined {
		names = append(names, name)
		seen[name] = true
	}
	for name := range repos {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		row := agentProfileSummaryRow{Name: name, Auto: auto[name], Repos: repos[name]}
		if configErr == nil {
			row.Problem = agentProfileProblem(name, defined)
		}
		view.Rows = append(view.Rows, row)
	}
	view.Rows = append(view.Rows, agentProfileSummaryRow{Name: agentProfileDefaultName, Repos: defaultRepos})
	return view
}

// agentProfileProblem describes why the named profile is unusable, or returns
// "" when it is usable. Only validation errors are shown, never env values.
func agentProfileProblem(name string, defined map[string]state.AgentProfile) string {
	profile, ok := defined[name]
	if !ok {
		return "UNDEFINED"
	}
	if !state.ValidAgentProfileName(name) {
		return "INVALID: name must be non-empty without surrounding whitespace"
	}
	if err := profile.Validate(); err != nil {
		return "INVALID: " + truncateStatusText(err.Error(), agentProfileReasonLimit)
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func truncateStatusText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// formatAgentProfileLabel is the one formatter for a watch target's profile
// field, shared by the plain, expanded, and TUI status output.
func formatAgentProfileLabel(profile watchTargetProfile) string {
	switch {
	case profile.Source == agentProfileSourceDefault:
		return "profile " + agentProfileDefaultName
	case profile.Unavailable():
		return fmt.Sprintf("profile %s (unavailable)", profile.Name)
	case profile.Source == agentProfileSourceAuto:
		return fmt.Sprintf("profile %s (auto)", profile.Name)
	default:
		return "profile " + profile.Name
	}
}

// agentProfileValidityUnknownNote is shown once when config.json cannot be
// read, so explicit profile names are displayed without validation.
const agentProfileValidityUnknownNote = "profile validity unknown: service config could not be read"

// formatAgentProfileSummaryRow renders one summary row, padding the name
// column to nameWidth.
func formatAgentProfileSummaryRow(row agentProfileSummaryRow, nameWidth int) string {
	return fmt.Sprintf("%-*s  %s", nameWidth, agentProfileSummaryName(row), agentProfileSummaryValue(row))
}

func agentProfileSummaryName(row agentProfileSummaryRow) string {
	if row.Auto {
		return row.Name + " (auto)"
	}
	return row.Name
}

func agentProfileSummaryValue(row agentProfileSummaryRow) string {
	repos := strings.Join(row.Repos, ", ")
	if row.Problem != "" {
		if repos == "" {
			return row.Problem
		}
		return fmt.Sprintf("%s (used by: %s)", row.Problem, repos)
	}
	if repos == "" {
		return "(unused)"
	}
	return repos
}

func agentProfileSummaryNameWidth(rows []agentProfileSummaryRow) int {
	width := 0
	for _, row := range rows {
		if n := len([]rune(agentProfileSummaryName(row))); n > width {
			width = n
		}
	}
	return width
}

// writeAgentProfilesSection writes the plain-text Agent profiles summary. It
// writes nothing when profiles are not in use.
func writeAgentProfilesSection(w io.Writer, view agentProfilesView) {
	if !view.Enabled {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Agent profiles")
	if view.ValidityUnknown {
		fmt.Fprintf(w, "  %s\n", agentProfileValidityUnknownNote)
	}
	width := agentProfileSummaryNameWidth(view.Rows)
	for _, row := range view.Rows {
		fmt.Fprintf(w, "  %s\n", formatAgentProfileSummaryRow(row, width))
	}
}
