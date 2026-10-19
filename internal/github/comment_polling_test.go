package ghcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

type commentPageRunner struct {
	mu      sync.Mutex
	calls   map[string]int
	failure bool
}

func (r *commentPageRunner) LookPath(s string) (string, error) { return s, nil }
func (r *commentPageRunner) Run(_ context.Context, _ string, _ string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	endpoint := args[1]
	r.calls[endpoint]++
	if r.failure {
		return "", errors.New("temporary failure")
	}
	if endpoint == "repos/o/r/issues/7/comments" {
		items := make([]map[string]any, 30)
		for i := range items {
			items[i] = map[string]any{"id": i + 1, "body": "old"}
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	}
	if endpoint == "repos/o/r/issues/7/comments?page=2" {
		return `[{"id":31,"body":"@vigilanteai recreate"}]`, nil
	}
	return `[]`, nil
}

func TestCommentPollingCachesPagesEmptyResultsAndErrors(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			base := &commentPageRunner{calls: map[string]int{}, failure: failure}
			var records []environment.AccessLogEntry
			runner := environment.LoggingRunner{Base: base, AccessLog: func(e environment.AccessLogEntry) { records = append(records, e) }}
			ctx, cache := WithCommentCache(context.Background())
			for _, purpose := range []string{"cleanup", "recreate", "resume", "iteration"} {
				comments, err := ListIssueCommentsForPolling(ctx, runner, "o/r", 7, purpose, nil)
				if failure {
					if err == nil {
						t.Fatal("lost error")
					}
				} else if err != nil || len(comments) != 31 || comments[30].Body != "@vigilanteai recreate" {
					t.Fatalf("%v %v", comments, err)
				}
				_, _ = ListPullRequestCommentsForPolling(ctx, runner, "o/r", 8, purpose, nil)
			}
			for endpoint, count := range base.calls {
				if count != 1 {
					t.Fatalf("%s fetched %d times", endpoint, count)
				}
			}
			expected := 3
			if failure {
				expected = 2
			}
			if len(records) != expected {
				t.Fatalf("access records=%d want %d", len(records), expected)
			}
			fetches, hits, failed := cache.Counts()
			if fetches != 2 || hits != 6 || (failure && failed != 2) {
				t.Fatal(fetches, hits, failed)
			}
			for _, record := range records {
				if record.Tool != "gh" || record.DurationMs < 0 {
					t.Fatal(record)
				}
			}
		})
	}
}

func TestCommentPollingCoalescesConcurrentReaders(t *testing.T) {
	base := &commentPageRunner{calls: map[string]int{}}
	ctx, _ := WithCommentCache(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = ListIssueCommentsForPolling(ctx, base, "o/r", 7, "cleanup", nil) }()
	}
	wg.Wait()
	if base.calls["repos/o/r/issues/7/comments"] != 1 || base.calls["repos/o/r/issues/7/comments?page=2"] != 1 {
		t.Fatal(base.calls)
	}
}

func TestCommentPollingSuppressesVerboseLoggingForRunnerVariants(t *testing.T) {
	for name, wrap := range map[string]func(environment.LoggingRunner) environment.Runner{
		"value":   func(r environment.LoggingRunner) environment.Runner { return r },
		"pointer": func(r environment.LoggingRunner) environment.Runner { return &r },
	} {
		t.Run(name, func(t *testing.T) {
			base := &commentPageRunner{calls: map[string]int{}}
			var records []environment.AccessLogEntry
			runner := wrap(environment.LoggingRunner{Base: base, AccessLog: func(e environment.AccessLogEntry) { records = append(records, e) }})
			if _, err := ListPullRequestCommentsForPolling(context.Background(), runner, "o/r", 8, "cleanup", nil); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 {
				t.Fatalf("access records=%d", len(records))
			}
		})
	}
}
