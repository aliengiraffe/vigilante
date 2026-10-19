package environment

import (
	"context"
	"sync"
)

type responseHeadersKey struct{}

// ResponseHeaders receives the HTTP status and ETag of the most recent
// budgeted `gh api` call made with a context carrying it. The budget runner
// consumes `--include` headers before callers see the body, so this is the
// channel through which conditional-request callers learn whether GitHub
// answered 304 Not Modified and which validator to send next time.
type ResponseHeaders struct {
	mu         sync.Mutex
	StatusCode int
	ETag       string
}

// WithResponseHeaders attaches a fresh header sink to ctx.
func WithResponseHeaders(ctx context.Context) (context.Context, *ResponseHeaders) {
	if ctx == nil {
		ctx = context.Background()
	}
	sink := &ResponseHeaders{}
	return context.WithValue(ctx, responseHeadersKey{}, sink), sink
}

// ResponseHeadersFromContext returns the sink attached by WithResponseHeaders, or nil.
func ResponseHeadersFromContext(ctx context.Context) *ResponseHeaders {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(responseHeadersKey{}).(*ResponseHeaders)
	return sink
}

func (h *ResponseHeaders) set(status int, etag string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.StatusCode = status
	h.ETag = etag
}

// Snapshot returns the recorded status and ETag.
func (h *ResponseHeaders) Snapshot() (int, string) {
	if h == nil {
		return 0, ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.StatusCode, h.ETag
}
