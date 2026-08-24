package realuptimeerrors

import "net/http"

// Middleware returns net/http middleware that: (1) stashes c into the
// request's context so downstream handlers can reach it with
// FromContext(r.Context()), and (2) recovers a panic escaping the handler
// below it, reports it with minimal request context (method, path -- never
// headers, cookies or bodies by default), answers with a 500, and does NOT
// re-panic (an HTTP server's job is to keep serving other requests; Go's
// own net/http server already turns an unrecovered panic into a dropped
// connection, which this middleware exists to prevent).
//
//	mux := http.NewServeMux()
//	...
//	handler := realuptimeerrors.Middleware(client)(mux)
//	http.ListenAndServe(":8080", handler)
func Middleware(c *Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithClient(r.Context(), c)
			r = r.WithContext(ctx)
			defer func() {
				if rec := recover(); rec != nil {
					c.RecoverWithoutRePanic(rec, WithRequest(requestContext(r)))
					c.Flush()
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// requestContext builds the minimal request context (method, path) shared
// by Middleware and any adapter built on top of it. Never headers, cookies
// or bodies: that is the same minimal default the JS, Python and Ruby SDKs
// use.
func requestContext(r *http.Request) WireRequest {
	req := WireRequest{Method: r.Method}
	if r.URL != nil {
		req.Path = r.URL.Path
	}
	return req
}
