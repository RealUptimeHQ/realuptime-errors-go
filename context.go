package realuptimeerrors

import "context"

type contextKey struct{}

// WithClient returns a context carrying c, for handlers and downstream
// calls to pick up with FromContext.
func WithClient(ctx context.Context, c *Client) context.Context {
	return context.WithValue(ctx, contextKey{}, c)
}

// FromContext returns the Client stashed by WithClient (typically by
// Middleware), or an inert no-op Client if none is present. Never returns
// nil, so callers never need a nil check before calling a method on the
// result.
func FromContext(ctx context.Context) *Client {
	if ctx != nil {
		if c, ok := ctx.Value(contextKey{}).(*Client); ok && c != nil {
			return c
		}
	}
	return &Client{logFn: func(string) {}}
}
