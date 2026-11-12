package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

const statusProfileSecret = "do-not-print-this-secret"

func statusProfileConfig() state.ServiceConfig {
	return state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"client-a": {Providers: map[string]state.AgentInvocation{
			"claude": {Command: "claude", Env: map[string]string{"CLIENT_TOKEN": statusProfileSecret}},
		}},
		"unused":      {Providers: map[string]state.AgentInvocation{"codex": {}}},
		"broken":      {Providers: map[string]state.AgentInvocation{"claud": {}}},
		"idle-broken": {Providers: map[string]state.AgentInvocation{"nope": {}}},
	}}
}

func statusProfileRepos() []watchedRepoStatus {
	return []watchedRepoStatus{
		{Target: state.WatchTarget{Repo: "acme/x", Path: "/x", AgentProfile: "broken"}},
		{Target: state.WatchTarget{Repo: "client-a/api", Path: "/api", AgentProfile: "client-a"}},
		{Target: state.WatchTarget{Repo: "client-a/web", Path: "/web", AgentProfile: "client-a"}},
		{Target: state.WatchTarget{Repo: "ghost/repo", Path: "/ghost", AgentProfile: "ghost"}},
		{Target: state.WatchTarget{Repo: "me/dotfiles", Path: "/dotfiles"}},
	}
}

func TestApplyAgentProfilesResolvesEachTarget(t *testing.T) {
	repos := statusProfileRepos()
	view := applyAgentProfiles(repos, statusProfileConfig(), nil)
	if !view.Enabled || view.ValidityUnknown {
		t.Fatalf("unexpected view flags: %+v", view)
	}
	want := map[string]string{
		"acme/x":       "profile broken (unavailable)",
		"client-a/api": "profile client-a",
		"client-a/web": "profile client-a",
		"ghost/repo":   "profile ghost (unavailable)",
		"me/dotfiles":  "profile default",
	}
	for _, repo := range repos {
		if repo.Profile == nil {
			t.Fatalf("%s: profile not set", repo.Target.Repo)
		}
		if got := formatAgentProfileLabel(*repo.Profile); got != want[repo.Target.Repo] {
			t.Errorf("%s: label = %q, want %q", repo.Target.Repo, got, want[repo.Target.Repo])
		}
	}
}

func TestApplyAgentProfilesSummaryRows(t *testing.T) {
	view := applyAgentProfiles(statusProfileRepos(), statusProfileConfig(), nil)
	var out bytes.Buffer
	writeAgentProfilesSection(&out, view)
	got := out.String()
	supported := strings.Join(state.AgentProfileProviderIDs(), ", ")
	want := "\nAgent profiles\n" +
		"  broken       INVALID: unknown provider \"claud\" (supported: " + supported + ") (used by: acme/x)\n" +
		"  client-a     client-a/api, client-a/web\n" +
		"  ghost        UNDEFINED (used by: ghost/repo)\n" +
		"  idle-broken  INVALID: unknown provider \"nope\" (supported: " + supported + ")\n" +
		"  unused       (unused)\n" +
		"  default      me/dotfiles\n"
	if got != want {
		t.Fatalf("summary mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, statusProfileSecret) {
		t.Fatal("summary must never include env values")
	}
}

func TestApplyAgentProfilesTruncatesLongReasons(t *testing.T) {
	long := strings.Repeat("x", 200)
	config := state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"long": {Providers: map[string]state.AgentInvocation{long: {}}},
	}}
	view := applyAgentProfiles(nil, config, nil)
	if len(view.Rows) != 2 {
		t.Fatalf("expected long + default rows, got %+v", view.Rows)
	}
	problem := view.Rows[0].Problem
	if !strings.HasPrefix(problem, "INVALID: ") || !strings.HasSuffix(problem, "…") {
		t.Fatalf("expected a truncated INVALID reason, got %q", problem)
	}
	if n := len([]rune(strings.TrimPrefix(problem, "INVALID: "))); n != agentProfileReasonLimit {
		t.Fatalf("reason length = %d, want %d", n, agentProfileReasonLimit)
	}
	if got := agentProfileSummaryValue(view.Rows[1]); got != "(unused)" {
		t.Fatalf("default row without repos = %q, want (unused)", got)
	}
}

func TestApplyAgentProfilesDisabledWithoutProfiles(t *testing.T) {
	repos := []watchedRepoStatus{{Target: state.WatchTarget{Repo: "owner/repo", Branch: "main", Provider: "codex"}}}
	before := formatWatchTargetRow(repos[0])
	view := applyAgentProfiles(repos, state.ServiceConfig{}, nil)
	if view.Enabled {
		t.Fatalf("expected profiles to be disabled, got %+v", view)
	}
	if repos[0].Profile != nil {
		t.Fatal("expected no profile on the status when profiles are unused")
	}
	if after := formatWatchTargetRow(repos[0]); after != before || strings.Contains(after, "profile") {
		t.Fatalf("row changed without profiles: %q -> %q", before, after)
	}
	var out bytes.Buffer
	writeAgentProfilesSection(&out, view)
	if out.Len() != 0 {
		t.Fatalf("expected no summary section, got %q", out.String())
	}
}

func TestApplyAgentProfilesEnabledByDefinitionAlone(t *testing.T) {
	repos := []watchedRepoStatus{{Target: state.WatchTarget{Repo: "owner/repo"}}}
	view := applyAgentProfiles(repos, state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{"a": {}}}, nil)
	if !view.Enabled {
		t.Fatal("a defined profile should enable the profile display")
	}
	if !strings.Contains(formatWatchTargetRow(repos[0]), "profile default") {
		t.Fatalf("expected profile default, got %q", formatWatchTargetRow(repos[0]))
	}
}

func TestApplyAgentProfilesWithUnreadableConfig(t *testing.T) {
	repos := []watchedRepoStatus{
		{Target: state.WatchTarget{Repo: "client-a/api", AgentProfile: "client-a"}},
		{Target: state.WatchTarget{Repo: "me/dotfiles"}},
	}
	view := applyAgentProfiles(repos, state.ServiceConfig{}, errors.New("config unreadable"))
	if !view.Enabled || !view.ValidityUnknown {
		t.Fatalf("expected enabled view with unknown validity, got %+v", view)
	}
	if got := formatAgentProfileLabel(*repos[0].Profile); got != "profile client-a" {
		t.Fatalf("explicit profile label = %q", got)
	}
	if got := formatAgentProfileLabel(*repos[1].Profile); got != "profile default" {
		t.Fatalf("default profile label = %q", got)
	}
	var out bytes.Buffer
	writeAgentProfilesSection(&out, view)
	got := out.String()
	if strings.Count(got, agentProfileValidityUnknownNote) != 1 {
		t.Fatalf("expected one validity note, got:\n%s", got)
	}
	if strings.Contains(got, "INVALID") || strings.Contains(got, "UNDEFINED") || strings.Contains(got, "unavailable") {
		t.Fatalf("validity must not be reported without config, got:\n%s", got)
	}

	// An unreadable config with no referenced profile keeps output unchanged.
	plain := []watchedRepoStatus{{Target: state.WatchTarget{Repo: "me/dotfiles"}}}
	if applyAgentProfiles(plain, state.ServiceConfig{}, errors.New("config unreadable")).Enabled {
		t.Fatal("expected profiles disabled when nothing references one")
	}
}

func TestFormatAgentProfileLabelAuto(t *testing.T) {
	if got := formatAgentProfileLabel(watchTargetProfile{Name: "oss", Source: agentProfileSourceAuto}); got != "profile oss (auto)" {
		t.Fatalf("auto label = %q", got)
	}
}

func TestStatusCommandShowsAgentProfiles(t *testing.T) {
	app, stdout := newPlainStatusTestApp(t)
	t.Cleanup(func() { _ = app.state.Close() })
	if err := app.state.SaveServiceConfig(statusProfileConfig()); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{
		{Repo: "client-a/api", Path: "/api", Branch: "main", Provider: "claude", AgentProfile: "client-a"},
		{Repo: "me/dotfiles", Path: "/dotfiles", Branch: "main", Provider: "codex"},
	}); err != nil {
		t.Fatal(err)
	}

	if exitCode := app.Run(context.Background(), []string{"status"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	output := stdout.String()
	for _, want := range []string{
		"client-a/api (branch main (pinned), provider claude, profile client-a, idle",
		"me/dotfiles (branch main (pinned), provider codex, profile default, idle",
		"Agent profiles\n",
		"  client-a     client-a/api\n",
		"  default      me/dotfiles\n",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, statusProfileSecret) || strings.Contains(output, "\x1b") {
		t.Fatalf("output leaked env values or ANSI escapes:\n%s", output)
	}
}

func TestStatusCommandOmitsAgentProfilesWhenUnused(t *testing.T) {
	app, stdout := newPlainStatusTestApp(t)
	t.Cleanup(func() { _ = app.state.Close() })
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Repo: "me/dotfiles", Path: "/dotfiles", Branch: "main", Provider: "codex"}}); err != nil {
		t.Fatal(err)
	}
	if exitCode := app.Run(context.Background(), []string{"status"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if output := stdout.String(); strings.Contains(output, "profile") || strings.Contains(output, "Agent profiles") {
		t.Fatalf("expected no profile output, got:\n%s", output)
	}
}

func TestLoadStatusSessionsIncludesAgentProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(filepath.Join(home, ".vigilante"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := New()
	t.Cleanup(func() { _ = app.state.Close() })
	app.stderr = testutil.IODiscard{}
	if err := app.state.SaveServiceConfig(statusProfileConfig()); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Repo: "acme/x", Path: "/x", AgentProfile: "broken"}}); err != nil {
		t.Fatal(err)
	}
	snapshot := app.loadStatusSessions(context.Background())
	if !snapshot.Profiles.Enabled || len(snapshot.Repos) != 1 || snapshot.Repos[0].Profile == nil || !snapshot.Repos[0].Profile.Unavailable() {
		t.Fatalf("expected an unavailable profile in the snapshot, got %+v", snapshot)
	}
}

func profileStatusModel(width int) statusModel {
	model := loadedStatusModel()
	model.width = width
	model.height = 80
	repos := statusProfileRepos()
	model.sessions.Profiles = applyAgentProfiles(repos, statusProfileConfig(), nil)
	model.sessions.Repos = repos
	return model
}

func TestStatusModelShowsAgentProfiles(t *testing.T) {
	model := profileStatusModel(140)
	view := model.View()
	for _, want := range []string{
		"Agent profiles",
		"profile client-a · branch",
		"profile default · branch",
		"profile broken (unavailable)",
		"UNDEFINED (used by: ghost/repo)",
		"(unused)",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("expected view to contain %q, got:\n%s", want, view)
		}
	}
	if strings.Contains(view, statusProfileSecret) {
		t.Fatal("dashboard must never include env values")
	}
}

func TestStatusModelStylesUnavailableProfileAsWarning(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	model := profileStatusModel(140)
	lines := strings.Join(model.repoLines(), "\n")
	if !strings.Contains(lines, statusWarnStyle.Render("profile broken (unavailable)")) {
		t.Fatalf("expected the unavailable profile in warning style, got %q", lines)
	}
	if strings.Contains(lines, statusWarnStyle.Render("profile client-a")) {
		t.Fatalf("usable profiles must not use the warning style, got %q", lines)
	}
}

func TestStatusModelOmitsAgentProfilesPaneWhenUnused(t *testing.T) {
	if view := loadedStatusModel().View(); strings.Contains(view, "Agent profiles") || strings.Contains(view, "profile default") {
		t.Fatalf("expected no profile output, got:\n%s", view)
	}
}

func TestStatusModelAgentProfilesPaneFitsNarrowTerminals(t *testing.T) {
	for _, width := range []int{30, 50} {
		model := profileStatusModel(width)
		for _, line := range model.contentLines() {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("width %d: line overflows (%d): %q", width, w, line)
			}
		}
	}
}
