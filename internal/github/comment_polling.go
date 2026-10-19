package ghcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

type commentCacheKey struct{}
type commentResult struct {
	output string
	err    error
}

// CommentCache shares complete comment snapshots, including errors and empty
// results, across handlers. Its lock also coalesces concurrent maintenance reads.
type CommentCache struct {
	mu                      sync.Mutex
	results                 map[string]commentResult
	fetches, hits, failures int
}

// WithCommentCache installs a fresh scan-scoped comment cache.
func WithCommentCache(ctx context.Context) (context.Context, *CommentCache) {
	c := &CommentCache{results: make(map[string]commentResult)}
	return context.WithValue(ctx, commentCacheKey{}, c), c
}

// Counts returns logical fetches, reused snapshots, and failed fetches.
func (c *CommentCache) Counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches, c.hits, c.failures
}

func fetchCommentPages(ctx context.Context, runner environment.Runner, path string) (string, error) {
	c, _ := ctx.Value(commentCacheKey{}).(*CommentCache)
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if result, ok := c.results[path]; ok {
			c.hits++
			return result.output, result.err
		}
	}
	// Preserve access logging and telemetry, suppress only verbose command text.
	// In particular do not unwrap the runner: its base may enforce quota.
	switch r := runner.(type) {
	case environment.LoggingRunner:
		r.Logger = nil
		r.LogSuccessOutput = false
		runner = r
	case *environment.LoggingRunner:
		copy := *r
		copy.Logger = nil
		copy.LogSuccessOutput = false
		runner = copy
	}
	output, err := readArrayPages(ctx, runner, path)
	if c != nil {
		c.fetches++
		if err != nil {
			c.failures++
		}
		c.results[path] = commentResult{output, err}
	}
	return output, err
}

func readArrayPages(ctx context.Context, runner environment.Runner, path string) (string, error) {
	var all []json.RawMessage
	for page := 1; ; page++ {
		endpoint := path
		if page > 1 {
			endpoint = fmt.Sprintf("%s?page=%d", path, page)
		}
		output, err := runner.Run(ctx, "", "gh", "api", endpoint)
		if err != nil {
			return "", err
		}
		var items []json.RawMessage
		if err = json.Unmarshal([]byte(output), &items); err != nil {
			return "", fmt.Errorf("parse comment page: %w", err)
		}
		all = append(all, items...)
		// GitHub's default page size is 30. Each page gets its own quota reservation
		// and access record; no hidden --paginate requests can overshoot the budget.
		if len(items) < 30 {
			break
		}
	}
	output, err := json.Marshal(all)
	return string(output), err
}
