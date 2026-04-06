package linear

import (
	"context"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestListOpenWorkItemsHydratesIssueViewsFromListIDs(t *testing.T) {
	runner := environment.Runner(testutil.FakeRunner{
		Outputs: map[string]string{
			"linear issue list": `ID       Title
ENG-12   First issue
ENG-3    Second issue
`,
			"linear issue view ENG-12 --json": `{"identifier":"ENG-12","title":"First issue","createdAt":"2026-04-01T10:00:00Z","url":"https://linear.app/issue/ENG-12","state":{"name":"Todo"},"labels":[{"name":"bug"}]}`,
			"linear issue view ENG-3 --json":  `{"identifier":"ENG-3","title":"Second issue","createdAt":"2026-04-02T10:00:00Z","url":"https://linear.app/issue/ENG-3","state":{"name":"In Progress"},"labels":[]}`,
		},
	})
	backend := NewBackend(&runner)

	items, err := backend.ListOpenWorkItems(context.Background(), "", "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Number != 12 || items[0].Title != "First issue" || items[0].Stage != "Todo" {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
	if !items[0].CreatedAt.Equal(time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected first item createdAt: %v", items[0].CreatedAt)
	}
	if items[1].Number != 3 || items[1].Title != "Second issue" || items[1].Stage != "In Progress" {
		t.Fatalf("unexpected second item: %#v", items[1])
	}
}

func TestParseLinearIssueListIDs(t *testing.T) {
	raw := `
ID        Title           State
ENG-101   Example one     Todo
ENG-55    Example two     In Progress
`

	ids := parseLinearIssueListIDs(raw)
	if len(ids) != 2 || ids[0] != "ENG-101" || ids[1] != "ENG-55" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestParseLinearIssueListIDsWhenIDColumnIsNotFirst(t *testing.T) {
	raw := `◌   ID       TITLE                                                              LABELS            E STATE UPDATED
--- ENG-1070 Integrate PostHog Logs                                                               - Todo  17 minutes ago
--- ENG-504  System Status - Add pop-up message for outage/LLM disruption mo... UI, notifications 1 Todo  39 days ago`

	ids := parseLinearIssueListIDs(raw)
	if len(ids) != 2 || ids[0] != "ENG-1070" || ids[1] != "ENG-504" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}
