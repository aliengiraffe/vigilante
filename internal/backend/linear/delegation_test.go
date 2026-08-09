package linear

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func newTestBackend(runner environment.Runner) *Backend {
	var r environment.Runner = runner
	return NewBackend(&r)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestBackendIDAndInterfaces(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{})
	var _ backend.IssueTracker = b
	var _ backend.LabelManager = b

	if b.ID() != backend.BackendLinear {
		t.Fatalf("ID() = %q, want %q", b.ID(), backend.BackendLinear)
	}
}

// Linear's CLI has no assignee resolution step, so an empty assignee has to
// become the sentinel the list filter understands rather than staying empty.
func TestResolveAssignee(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{})

	tests := map[string]string{
		"":          "me",
		"   ":       "me",
		"someuser":  "someuser",
		"  padded ": "padded",
	}
	for input, want := range tests {
		got, err := b.ResolveAssignee(context.Background(), input)
		if err != nil {
			t.Fatalf("ResolveAssignee(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("ResolveAssignee(%q) = %q, want %q", input, got, want)
		}
	}
}

// Listing is two-phase: a plain-text list to harvest identifiers, then one JSON
// view per identifier. The list output is human-formatted, so the ID extraction
// has to survive headers and separator rules.
func TestListOpenWorkItems(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue list":               "ID      TITLE\n─────── ─────\nENG-12  First\nENG-34  Second\n",
			"linear issue view ENG-12 --json": `{"identifier":"ENG-12","title":" First ","url":"https://linear.app/x/ENG-12","createdAt":"2026-03-10T12:00:00Z","state":{"name":" In Progress "}}`,
			"linear issue view ENG-34 --json": `{"number":34,"title":"Second","url":"u34","createdAt":"2026-03-11T12:00:00Z"}`,
		},
	})

	items, err := b.ListOpenWorkItems(context.Background(), "team", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	// Number is derived from the identifier suffix when the payload omits it.
	if items[0].Number != 12 {
		t.Errorf("items[0].Number = %d, want 12 derived from ENG-12", items[0].Number)
	}
	if items[0].Title != "First" {
		t.Errorf("items[0].Title = %q, want the trimmed title", items[0].Title)
	}
	if items[0].Stage != "In Progress" {
		t.Errorf("items[0].Stage = %q, want the trimmed state name", items[0].Stage)
	}
	if !items[0].CreatedAt.Equal(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("items[0].CreatedAt = %v, want the parsed timestamp", items[0].CreatedAt)
	}

	// An explicit number in the payload wins over the identifier.
	if items[1].Number != 34 {
		t.Errorf("items[1].Number = %d, want 34", items[1].Number)
	}
	if items[1].Stage != "" {
		t.Errorf("items[1].Stage = %q, want empty when the payload has no state", items[1].Stage)
	}
}

func TestListOpenWorkItemsAcceptsMeAsAssignee(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue list":              "ENG-1\n",
			"linear issue view ENG-1 --json": `{"identifier":"ENG-1","title":"t"}`,
		},
	})

	if _, err := b.ListOpenWorkItems(context.Background(), "team", "me"); err != nil {
		t.Fatalf(`assignee "me" must be accepted: %v`, err)
	}
}

// The Linear backend cannot filter by an arbitrary assignee. Returning an
// explicit error beats silently returning every issue in the team.
func TestListOpenWorkItemsRejectsOtherAssignees(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue list":              "ENG-1\n",
			"linear issue view ENG-1 --json": `{"identifier":"ENG-1","title":"t"}`,
		},
	})

	items, err := b.ListOpenWorkItems(context.Background(), "team", "someone-else")
	if err == nil {
		t.Fatal("expected an unsupported-filter error")
	}
	if items != nil {
		t.Fatalf("items must be nil when the filter is unsupported, got %#v", items)
	}
}

func TestListOpenWorkItemsEmptyList(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{"linear issue list": "\n"},
	})

	items, err := b.ListOpenWorkItems(context.Background(), "team", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

func TestListOpenWorkItemsPropagatesErrors(t *testing.T) {
	t.Parallel()

	t.Run("list failure", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Errors: map[string]error{"linear issue list": errors.New("boom")},
		})
		if _, err := b.ListOpenWorkItems(context.Background(), "team", ""); err == nil {
			t.Fatal("expected the list error to propagate")
		}
	})

	t.Run("per-issue view failure", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{"linear issue list": "ENG-1\n"},
			Errors:  map[string]error{"linear issue view ENG-1 --json": errors.New("boom")},
		})
		if _, err := b.ListOpenWorkItems(context.Background(), "team", ""); err == nil {
			t.Fatal("expected the view error to propagate")
		}
	})

	t.Run("malformed per-issue JSON", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{
				"linear issue list":              "ENG-1\n",
				"linear issue view ENG-1 --json": "not json",
			},
		})
		if _, err := b.ListOpenWorkItems(context.Background(), "team", ""); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestGetWorkItemDetails(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue view 42 --json": `{"title":" T ","description":" D ","url":" u ","labels":[{"name":"bug"}]}`,
		},
	})

	details, err := b.GetWorkItemDetails(context.Background(), "team", 42)
	if err != nil {
		t.Fatal(err)
	}
	if details.Title != "T" || details.Body != "D" || details.URL != "u" {
		t.Errorf("fields not trimmed: %#v", details)
	}
	// Linear has no closed/open distinction in this payload, so the backend
	// normalizes to "open"; the orchestration loop relies on that.
	if details.State != "open" {
		t.Errorf("State = %q, want open", details.State)
	}
	if len(details.Labels) != 1 || details.Labels[0].Name != "bug" {
		t.Errorf("Labels = %#v, want one bug label", details.Labels)
	}
}

func TestGetWorkItemDetailsErrors(t *testing.T) {
	t.Parallel()

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Errors: map[string]error{"linear issue view 42 --json": errors.New("boom")},
		})
		if _, err := b.GetWorkItemDetails(context.Background(), "team", 42); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{"linear issue view 42 --json": "{oops"},
		})
		if _, err := b.GetWorkItemDetails(context.Background(), "team", 42); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestListWorkItemComments(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue comment list 42 --json": `[
				{"id":1,"body":" hello ","createdAt":"2026-03-10T12:00:00Z","user":{"name":" Ann "}},
				{"id":2,"body":"second","createdAt":"bogus","user":{"name":"Bob"}}
			]`,
		},
	})

	comments, err := b.ListWorkItemComments(context.Background(), "team", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	if comments[0].Body != "hello" {
		t.Errorf("Body = %q, want trimmed", comments[0].Body)
	}
	// Linear reports an author name where the neutral type carries a login.
	if comments[0].User.Login != "Ann" {
		t.Errorf("User.Login = %q, want the trimmed Linear user name", comments[0].User.Login)
	}
	// An unparseable timestamp must not fail the whole listing; it degrades to
	// the zero time so polling still sees the comment.
	if !comments[1].CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want the zero time for an unparseable timestamp", comments[1].CreatedAt)
	}
}

func TestListWorkItemCommentsEmptyAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("empty output", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{"linear issue comment list 42 --json": "  "},
		})
		comments, err := b.ListWorkItemComments(context.Background(), "team", 42)
		if err != nil {
			t.Fatal(err)
		}
		if len(comments) != 0 {
			t.Fatalf("got %d comments, want 0", len(comments))
		}
	})

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Errors: map[string]error{"linear issue comment list 42 --json": errors.New("boom")},
		})
		if _, err := b.ListWorkItemComments(context.Background(), "team", 42); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{"linear issue comment list 42 --json": "["},
		})
		if _, err := b.ListWorkItemComments(context.Background(), "team", 42); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestListWorkItemCommentsForPollingMatchesPlainListing(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue comment list 42 --json": `[{"id":1,"body":"hi","user":{"name":"Ann"}}]`,
		},
	}

	plain, err := newTestBackend(runner).ListWorkItemComments(context.Background(), "team", 42)
	if err != nil {
		t.Fatal(err)
	}
	polled, err := newTestBackend(runner).ListWorkItemCommentsForPolling(context.Background(), "team", 42, "purpose", discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(plain) != len(polled) || len(polled) != 1 || polled[0].ID != plain[0].ID {
		t.Fatalf("polling listing %#v should match the plain listing %#v", polled, plain)
	}
}

func TestCommentOnWorkItem(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{"linear issue comment add 42 --body hello": ""},
	})
	if err := b.CommentOnWorkItem(context.Background(), "team", 42, "hello"); err != nil {
		t.Fatal(err)
	}

	failing := newTestBackend(testutil.FakeRunner{
		Errors: map[string]error{"linear issue comment add 42 --body hello": errors.New("boom")},
	})
	if err := failing.CommentOnWorkItem(context.Background(), "team", 42, "hello"); err == nil {
		t.Fatal("expected the error to propagate")
	}
}

func TestCreateWorkItem(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue create --title T --description B --json": `{"identifier":"ENG-77","url":" https://linear.app/x/ENG-77 "}`,
		},
	})

	// Labels and assignees are accepted but unsupported by the Linear CLI path;
	// passing them must not change the command.
	created, err := b.CreateWorkItem(context.Background(), "team", "T", "B", []string{"ignored"}, []string{"ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 77 {
		t.Errorf("Number = %d, want 77 derived from ENG-77", created.Number)
	}
	if created.URL != "https://linear.app/x/ENG-77" {
		t.Errorf("URL = %q, want trimmed", created.URL)
	}
}

func TestCreateWorkItemErrors(t *testing.T) {
	t.Parallel()

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Errors: map[string]error{
				"linear issue create --title T --description B --json": errors.New("boom"),
			},
		})
		if _, err := b.CreateWorkItem(context.Background(), "team", "T", "B", nil, nil); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(testutil.FakeRunner{
			Outputs: map[string]string{
				"linear issue create --title T --description B --json": "nope",
			},
		})
		if _, err := b.CreateWorkItem(context.Background(), "team", "T", "B", nil, nil); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

// Linear has no "not planned" close reason, so cancelling is the closest
// equivalent to how the GitHub backend closes an abandoned item.
func TestCloseWorkItemCancels(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{"linear issue update 42 --state Canceled": ""},
	})
	if err := b.CloseWorkItem(context.Background(), "team", 42); err != nil {
		t.Fatal(err)
	}

	failing := newTestBackend(testutil.FakeRunner{
		Errors: map[string]error{"linear issue update 42 --state Canceled": errors.New("boom")},
	})
	if err := failing.CloseWorkItem(context.Background(), "team", 42); err == nil {
		t.Fatal("expected the error to propagate")
	}
}

func TestIsWorkItemUnavailable(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{})

	if b.IsWorkItemUnavailable(nil) {
		t.Error("nil must not be reported as unavailable")
	}
	for _, text := range []string{"Issue not found", "NOT FOUND", "no such issue"} {
		if !b.IsWorkItemUnavailable(errors.New(text)) {
			t.Errorf("%q should be reported as unavailable", text)
		}
	}
	for _, text := range []string{"rate limited", "internal server error"} {
		if b.IsWorkItemUnavailable(errors.New(text)) {
			t.Errorf("%q must not be reported as unavailable", text)
		}
	}
}

// Linear has no repository-label concept. These are deliberate no-ops rather
// than unimplemented errors, so the shared orchestration loop can call them
// unconditionally.
func TestLabelOperationsAreNoOps(t *testing.T) {
	t.Parallel()

	// An empty runner fails on any command, so reaching nil proves no call was made.
	b := newTestBackend(testutil.FakeRunner{})
	ctx := context.Background()

	if err := b.EnsureProjectLabels(ctx, "team", []backend.RepositoryLabelSpec{{Name: "x"}}); err != nil {
		t.Errorf("EnsureProjectLabels should be a no-op, got %v", err)
	}
	if err := b.SyncWorkItemLabels(ctx, "team", 1, []backend.Label{{Name: "a"}}, []string{"b"}, []string{"c"}); err != nil {
		t.Errorf("SyncWorkItemLabels should be a no-op, got %v", err)
	}
	if err := b.RemoveWorkItemLabel(ctx, "team", 1, "lbl"); err != nil {
		t.Errorf("RemoveWorkItemLabel should be a no-op, got %v", err)
	}
	if err := b.AddCommentReaction(ctx, "team", 1, "+1"); err != nil {
		t.Errorf("AddCommentReaction should be a no-op, got %v", err)
	}
}

// The list output is a human-formatted table. Header rows, box-drawing rules, and
// blank lines all have to be skipped or they become bogus issue identifiers.
func TestParseLinearIssueListIDsTableCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "whitespace only", raw: "   \n  \n", want: nil},
		{
			name: "header and ascii rule are skipped",
			raw:  "ID     TITLE\n------ -----\nENG-1  a\nENG-2  b",
			want: []string{"ENG-1", "ENG-2"},
		},
		{
			name: "unicode box rule is skipped",
			raw:  "ID\n─────\nENG-3",
			want: []string{"ENG-3"},
		},
		{
			name: "lines without an identifier are skipped",
			raw:  "no identifier here\nENG-4 ok",
			want: []string{"ENG-4"},
		},
		{
			name: "numeric-suffixed prefixes are matched",
			raw:  "AB1-9 x",
			want: []string{"AB1-9"},
		},
		{
			name: "lowercase prefixes are not identifiers",
			raw:  "eng-5 x",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseLinearIssueListIDs(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestParseLinearIssueNumber(t *testing.T) {
	t.Parallel()

	tests := map[string]int{
		"ENG-12":  12,
		"eng-7":   7,
		" ENG-8 ": 8,
		"ENG":     0,
		"":        0,
		"ENG-abc": 0,
		"A-B-99":  99,
	}
	for input, want := range tests {
		if got := parseLinearIssueNumber(input); got != want {
			t.Errorf("parseLinearIssueNumber(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestIsLinearIssueListSeparator(t *testing.T) {
	t.Parallel()

	separators := []string{"---", "───", "-- --", "   ", ""}
	for _, line := range separators {
		if !isLinearIssueListSeparator(line) {
			t.Errorf("%q should be treated as a separator", line)
		}
	}
	for _, line := range []string{"ENG-1", "-x-", "— em dash"} {
		if isLinearIssueListSeparator(line) {
			t.Errorf("%q must not be treated as a separator", line)
		}
	}
}

// parseLinearWorkItems is the batch variant of the single-item parser. It is not
// on the ListOpenWorkItems path today, so it needs direct coverage.
func TestParseLinearWorkItems(t *testing.T) {
	t.Parallel()

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()

		items, err := parseLinearWorkItems("")
		if err != nil {
			t.Fatal(err)
		}
		if items != nil {
			t.Fatalf("got %#v, want nil", items)
		}
	})

	t.Run("malformed input", func(t *testing.T) {
		t.Parallel()

		if _, err := parseLinearWorkItems("{"); err == nil {
			t.Fatal("expected a parse error")
		}
	})

	t.Run("mixed payload", func(t *testing.T) {
		t.Parallel()

		items, err := parseLinearWorkItems(`[
			{"identifier":"ENG-5","title":" A ","url":" u5 ","createdAt":"2026-03-10T12:00:00Z","state":{"name":" Todo "},"labels":[{"name":"l"}]},
			{"number":6,"title":"B","createdAt":"nope"}
		]`)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 {
			t.Fatalf("got %d items, want 2", len(items))
		}
		if items[0].Number != 5 || items[0].Title != "A" || items[0].URL != "u5" || items[0].Stage != "Todo" {
			t.Errorf("items[0] = %#v", items[0])
		}
		if len(items[0].Labels) != 1 {
			t.Errorf("labels not carried through: %#v", items[0].Labels)
		}
		if items[1].Number != 6 || !items[1].CreatedAt.IsZero() {
			t.Errorf("items[1] = %#v", items[1])
		}
	})
}

func TestBackendReadsRunnerThroughPointer(t *testing.T) {
	t.Parallel()

	var runner environment.Runner = testutil.FakeRunner{
		Errors: map[string]error{"linear issue update 1 --state Canceled": errors.New("first")},
	}
	b := NewBackend(&runner)

	if err := b.CloseWorkItem(context.Background(), "team", 1); err == nil {
		t.Fatal("expected the first runner to fail")
	}

	runner = testutil.FakeRunner{
		Outputs: map[string]string{"linear issue update 1 --state Canceled": ""},
	}
	if err := b.CloseWorkItem(context.Background(), "team", 1); err != nil {
		t.Fatalf("the replaced runner should be used: %v", err)
	}
}
