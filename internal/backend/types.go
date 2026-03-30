package backend

import "time"

// BackendID identifies a project management backend.
type BackendID string

const (
	// BackendGitHub is the GitHub backend identifier.
	BackendGitHub BackendID = "github"
	// BackendLinear is the Linear backend identifier (not yet implemented).
	BackendLinear BackendID = "linear"
	// BackendJira is the Jira backend identifier (not yet implemented).
	BackendJira BackendID = "jira"
)

// WorkItem represents a work item from any project management backend.
// For GitHub this maps to an issue; for Linear it would map to an issue;
// for Jira it would map to a ticket.
type WorkItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	Labels    []Label   `json:"labels"`
	Stage     string    `json:"stage,omitempty"`
}

// Label represents a label or tag on a work item.
type Label struct {
	Name string `json:"name"`
}

// WorkItemDetails contains the full details of a work item.
type WorkItemDetails struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"html_url"`
	State     string    `json:"state"`
	Labels    []Label   `json:"labels"`
	Assignees []UserRef `json:"assignees"`
}

// UserRef is a reference to a user identity in a backend system.
type UserRef struct {
	Login string `json:"login"`
}

// WorkItemComment is a comment on a work item.
type WorkItemComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// PullRequest represents a pull request or merge request.
type PullRequest struct {
	Number            int           `json:"number"`
	Title             string        `json:"title"`
	Body              string        `json:"body"`
	URL               string        `json:"url"`
	State             string        `json:"state"`
	BaseRefName       string        `json:"baseRefName"`
	MergedAt          *time.Time    `json:"mergedAt"`
	Labels            []Label       `json:"labels"`
	IsDraft           bool          `json:"isDraft"`
	Mergeable         string        `json:"mergeable"`
	MergeStateStatus  string        `json:"mergeStateStatus"`
	ReviewDecision    string        `json:"reviewDecision"`
	StatusCheckRollup []StatusCheck `json:"statusCheckRollup"`
}

// StatusCheck represents a CI status check on a pull request.
type StatusCheck struct {
	Context    string `json:"context"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
}

// CreatedWorkItem is the result of creating a new work item.
type CreatedWorkItem struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// RateLimitResource represents rate limit state for one API category.
type RateLimitResource struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// RateLimitSnapshot captures the current rate limit state across API categories.
type RateLimitSnapshot struct {
	Core    RateLimitResource
	Rate    RateLimitResource
	GraphQL RateLimitResource
	Search  RateLimitResource
}

// RepositoryLabelSpec defines a label to be created on a project.
type RepositoryLabelSpec struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// RepositoryLabelDetails is a label that exists on a project.
type RepositoryLabelDetails struct {
	Name string `json:"name"`
}

// ProgressComment is a structured progress update for posting to a work item.
type ProgressComment struct {
	Stage      string
	Emoji      string
	Percent    int
	ETAMinutes int
	Items      []string
	Tagline    string
}

// DispatchFailureComment is a structured dispatch failure update.
type DispatchFailureComment struct {
	Stage        string
	Summary      string
	Branch       string
	WorktreePath string
	NextStep     string
}
