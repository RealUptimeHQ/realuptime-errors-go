package realuptimeerrors

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Shared test seams: a fake clock and a recording/queueable fake HTTP
// transport, so no test touches the network or sleeps for real (the
// errors-ruby/test/test_helper.rb convention ported to Go).

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_000_000, 0).UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type queuedResponse struct {
	status int
	body   string
	err    error
}

type fakeServer struct {
	mu        sync.Mutex
	responses []queuedResponse
	calls     []map[string]any
}

func newFakeServer() *fakeServer { return &fakeServer{} }

func (s *fakeServer) respondJSON(status int, body map[string]any) *fakeServer {
	raw, _ := json.Marshal(body)
	s.mu.Lock()
	s.responses = append(s.responses, queuedResponse{status: status, body: string(raw)})
	s.mu.Unlock()
	return s
}

func (s *fakeServer) respondOK() *fakeServer { return s.respondJSON(200, map[string]any{}) }

func (s *fakeServer) failWith(err error) *fakeServer {
	s.mu.Lock()
	s.responses = append(s.responses, queuedResponse{err: err})
	s.mu.Unlock()
	return s
}

func (s *fakeServer) Do(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var batch map[string]any
	_ = json.Unmarshal(raw, &batch)

	s.mu.Lock()
	s.calls = append(s.calls, batch)
	var resp queuedResponse
	if len(s.responses) > 1 {
		resp, s.responses = s.responses[0], s.responses[1:]
	} else if len(s.responses) == 1 {
		resp = s.responses[0]
	} else {
		resp = queuedResponse{status: 200, body: "{}"}
	}
	s.mu.Unlock()

	if resp.err != nil {
		return nil, resp.err
	}
	return &http.Response{
		StatusCode: resp.status,
		Body:       io.NopCloser(newStringReader(resp.body)),
		Header:     make(http.Header),
	}, nil
}

func (s *fakeServer) batches() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.calls...)
}

func (s *fakeServer) events() []any {
	var out []any
	for _, b := range s.batches() {
		if events, ok := b["events"].([]any); ok {
			out = append(out, events...)
		}
	}
	return out
}

func (s *fakeServer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type stringReader struct {
	s string
	i int
}

func newStringReader(s string) *stringReader { return &stringReader{s: s} }

func (r *stringReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// testClient builds an inline (non-background) Client wired to a fake
// server and clock, mirroring setup_sdk in the Ruby suite.
func testClient(server *fakeServer, clock *fakeClock, logs *[]string) *Client {
	background := false
	sendDevice := false
	return NewClient(Config{
		DSN:            "https://realuptime.test/api/errors/v1/ingest/rue_test",
		Background:     &background,
		SendDeviceInfo: &sendDevice,
		Logger:         func(msg string) { *logs = append(*logs, msg) },
		httpClient:     server,
		now:            clock.Now,
	})
}
