package backend

import (
	"strings"
	"time"
)

// FindCommandComment searches for the newest unacknowledged Vigilante command
// comment matching the given command string (e.g. "@vigilanteai resume").
// If acknowledgedID is non-zero, that specific comment ID is treated as
// already handled and skipped.
func FindCommandComment(comments []Comment, command string, acknowledgedID int64) *Comment {
	want := normalizeVigilanteComment(command)
	for i := len(comments) - 1; i >= 0; i-- {
		body := normalizeVigilanteComment(comments[i].Body)
		if body != want {
			continue
		}
		if acknowledgedID != 0 && comments[i].ID == acknowledgedID {
			return nil
		}
		return &comments[i]
	}
	return nil
}

// FindResumeComment finds an unacknowledged @vigilanteai resume command.
func FindResumeComment(comments []Comment, acknowledgedID int64) *Comment {
	return FindCommandComment(comments, "@vigilanteai resume", acknowledgedID)
}

// FindCleanupComment finds an unacknowledged @vigilanteai cleanup command.
func FindCleanupComment(comments []Comment, acknowledgedID int64) *Comment {
	return FindCommandComment(comments, "@vigilanteai cleanup", acknowledgedID)
}

// FindRecreateComment finds an unacknowledged @vigilanteai recreate command.
func FindRecreateComment(comments []Comment, acknowledgedID int64) *Comment {
	return FindCommandComment(comments, "@vigilanteai recreate", acknowledgedID)
}

// FindIterationComment finds the newest unacknowledged @vigilanteai comment
// that is not a known command (resume, cleanup, recreate). It uses monotonic
// claiming: a comment is considered new only if it was created after
// claimedCommentAt, or if timestamps match, has a higher ID than claimedCommentID.
func FindIterationComment(comments []Comment, claimedCommentID int64, claimedCommentAt string) *Comment {
	claimedAt := parseClaimedCommentTime(claimedCommentAt)
	for i := len(comments) - 1; i >= 0; i-- {
		if !isCommentNewerThanClaim(comments[i], claimedAt, claimedCommentID) {
			continue
		}
		if !IsIterationComment(comments[i]) {
			continue
		}
		return &comments[i]
	}
	return nil
}

func parseClaimedCommentTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func isCommentNewerThanClaim(comment Comment, claimedAt time.Time, claimedCommentID int64) bool {
	if claimedCommentID == 0 && claimedAt.IsZero() {
		return true
	}
	commentAt := comment.CreatedAt.UTC()
	if !claimedAt.IsZero() {
		if commentAt.Before(claimedAt) {
			return false
		}
		if commentAt.After(claimedAt) {
			return true
		}
	}
	return comment.ID > claimedCommentID
}

// IsIterationComment reports whether a comment is a @vigilanteai iteration
// instruction (i.e. starts with @vigilanteai but is not a known command).
func IsIterationComment(comment Comment) bool {
	body := normalizeVigilanteComment(comment.Body)
	if !strings.HasPrefix(body, "@vigilanteai") {
		return false
	}
	if IsKnownVigilanteCommand(body) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(body, "@vigilanteai")) != ""
}

// IsKnownVigilanteCommand reports whether the normalized comment body matches
// a well-known Vigilante command.
func IsKnownVigilanteCommand(body string) bool {
	switch normalizeVigilanteComment(body) {
	case "@vigilanteai resume", "@vigilanteai cleanup", "@vigilanteai recreate":
		return true
	default:
		return false
	}
}

// AssigneeIterationComments filters iteration comments to those authored by
// one of the given assignee logins.
func AssigneeIterationComments(comments []Comment, assignees []string) []Comment {
	if len(assignees) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(assignees))
	for _, assignee := range assignees {
		login := strings.TrimSpace(strings.ToLower(assignee))
		if login == "" {
			continue
		}
		allowed[login] = struct{}{}
	}
	selected := make([]Comment, 0, len(comments))
	for _, comment := range comments {
		if !IsIterationComment(comment) {
			continue
		}
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(comment.Author))]; !ok {
			continue
		}
		selected = append(selected, comment)
	}
	return selected
}

// LatestUserCommentTime returns the timestamp of the most recent user-authored
// comment, or the zero time if none exist.
func LatestUserCommentTime(comments []Comment) time.Time {
	for i := len(comments) - 1; i >= 0; i-- {
		if IsUserComment(comments[i]) {
			return comments[i].CreatedAt.UTC()
		}
	}
	return time.Time{}
}

// IsUserComment reports whether a comment was written by a human user rather
// than automation.
func IsUserComment(comment Comment) bool {
	body := strings.TrimSpace(comment.Body)
	if body == "" {
		return false
	}
	if strings.HasPrefix(body, "@vigilanteai ") {
		return true
	}
	return !isAutomationComment(body)
}

func normalizeVigilanteComment(body string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(body)))
	return strings.Join(fields, " ")
}

func isAutomationComment(body string) bool {
	if !strings.HasPrefix(body, "## ") {
		return false
	}
	if strings.Contains(body, "\nProgress: [") && strings.Contains(body, "\n`ETA: ~") {
		return true
	}
	if strings.Contains(body, "\nWorking branch: `") || strings.Contains(body, "\nETA: ~") {
		return true
	}
	return false
}
