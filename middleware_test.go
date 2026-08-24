package realuptimeerrors

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareRecoversPanicReportsAnd500s(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Middleware must recover the panic, not let it escape: %v", r)
			}
		}()
		handler.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	events := server.events()
	if len(events) != 1 {
		t.Fatalf("expected 1 reported event, got %d", len(events))
	}
	event := events[0].(map[string]any)
	req2 := event["request"].(map[string]any)
	if req2["method"] != "GET" || req2["path"] != "/orders/42" {
		t.Fatalf("request context = %v, want method GET and path /orders/42", req2)
	}
}

func TestMiddlewareLetsANormalRequestThrough(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer()
	var logs []string
	c := testClient(server, clock, &logs)

	called := false
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected the wrapped handler to run")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(server.batches()) != 0 {
		t.Fatal("expected no report for a request that did not panic")
	}
}

func TestFromContextReturnsTheStashedClient(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	var seen *Client
	handler := Middleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen != c {
		t.Fatal("FromContext did not return the client Middleware stashed")
	}
}

func TestFromContextOnABareContextIsAnInertNoOp(t *testing.T) {
	c := FromContext(nil)
	if c == nil {
		t.Fatal("FromContext must never return nil")
	}
	c.CaptureMessage("should be a no-op")
}
