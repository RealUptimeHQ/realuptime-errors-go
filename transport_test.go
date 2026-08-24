package realuptimeerrors

import (
	"errors"
	"testing"
	"time"
)

func newInlineTransport(server *fakeServer, clock *fakeClock) *transport {
	return newTransport("https://realuptime.test/api/errors/v1/ingest/rue_test", transportOptions{
		background: false,
		httpClient: server,
		now:        clock.Now,
		log:        func(string) {},
	})
}

func TestTransportBoundedBufferDropsOldestAndCounts(t *testing.T) {
	server := newFakeServer() // never flushed inline here; we drive enqueue only
	clock := newFakeClock()
	tr := newTransport("https://realuptime.test/ingest", transportOptions{
		background: false,
		httpClient: server,
		now:        clock.Now,
		log:        func(string) {},
	})
	// Fill past BufferMax without ever succeeding, by pausing delivery via a
	// paused-until in the future -- simplest is to disable background and
	// avoid calling flush; enqueue with background:false calls flush every
	// time, so instead we drive the internal buffer directly through
	// enqueue while the (single, always-failing) response keeps every
	// event stuck. Use a server that always fails to keep the buffer full.
	server.failWith(errors.New("network unreachable"))
	for i := 0; i < BufferMax+5; i++ {
		tr.enqueue(WireEvent{Message: "m"})
	}
	if got := tr.bufferedCount(); got != BufferMax {
		t.Fatalf("buffered count = %d, want %d", got, BufferMax)
	}
	tr.mu.Lock()
	dropped := tr.droppedSinceReport
	tr.mu.Unlock()
	if dropped != 5 {
		t.Fatalf("droppedSinceReport = %d, want 5", dropped)
	}
}

func TestTransportReportsDroppedClientOnNextSuccessfulBatch(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	server.failWith(errors.New("network unreachable"))
	for i := 0; i < BufferMax+3; i++ {
		tr.enqueue(WireEvent{Message: "m"})
	}
	// Force the backoff window open so a subsequent flush actually tries.
	tr.mu.Lock()
	tr.nextAttemptAt = time.Time{}
	tr.mu.Unlock()
	server.respondOK()
	tr.flush()

	batches := server.batches()
	last := batches[len(batches)-1]
	dropped, _ := last["droppedClient"].(float64)
	if int(dropped) != 3 {
		t.Fatalf("droppedClient on successful batch = %v, want 3", dropped)
	}
}

func TestTransportExponentialBackoff(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	server.failWith(errors.New("boom"))
	tr.enqueue(WireEvent{Message: "m"})
	tr.mu.Lock()
	b1 := tr.backoff
	tr.mu.Unlock()
	if b1 != backoffStart {
		t.Fatalf("first backoff = %v, want %v", b1, backoffStart)
	}

	clock.Advance(backoffStart)
	server.failWith(errors.New("boom"))
	tr.flush()
	tr.mu.Lock()
	b2 := tr.backoff
	tr.mu.Unlock()
	if b2 != backoffStart*2 {
		t.Fatalf("second backoff = %v, want %v", b2, backoffStart*2)
	}

	// Backoff caps at backoffMax regardless of how many failures follow.
	tr.mu.Lock()
	tr.backoff = backoffMax
	tr.nextAttemptAt = time.Time{}
	tr.mu.Unlock()
	server.failWith(errors.New("boom"))
	tr.flush()
	tr.mu.Lock()
	b3 := tr.backoff
	tr.mu.Unlock()
	if b3 != backoffMax {
		t.Fatalf("capped backoff = %v, want %v", b3, backoffMax)
	}
}

func TestTransportOverQuotaPausesUntilReset(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	resetsAt := clock.Now().Add(time.Hour).Format(time.RFC3339)
	server.respondJSON(429, map[string]any{"reason": "over_quota", "resetsAt": resetsAt})
	tr.enqueue(WireEvent{Message: "m"})

	if got := tr.bufferedCount(); got != 0 {
		t.Fatalf("buffered count after over-quota drop = %d, want 0", got)
	}
	tr.mu.Lock()
	paused := tr.pausedUntil
	tr.mu.Unlock()
	if !paused.Equal(mustParseRFC3339(t, resetsAt)) {
		t.Fatalf("pausedUntil = %v, want %v", paused, resetsAt)
	}
}

func TestTransportKeyRevokedDisablesPermanently(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	server.respondJSON(403, map[string]any{"reason": "key_revoked"})
	tr.enqueue(WireEvent{Message: "m"})

	if !tr.isDisabled() {
		t.Fatal("expected transport to be disabled after key_revoked")
	}
	before := server.callCount()
	tr.enqueue(WireEvent{Message: "m2"})
	if server.callCount() != before {
		t.Fatal("a disabled transport must never make another request")
	}
}

func TestTransportMalformedBatchIsDroppedNotRetried(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	server.respondJSON(400, map[string]any{"error": "bad shape"})
	server.respondOK()
	tr.enqueue(WireEvent{Message: "m"})

	if got := tr.bufferedCount(); got != 0 {
		t.Fatalf("buffered count after malformed refusal = %d, want 0 (dropped, not retried)", got)
	}
}

func TestTransportBatchesAtMostFiftyEventsAtOnce(t *testing.T) {
	server := newFakeServer()
	clock := newFakeClock()
	tr := newInlineTransport(server, clock)

	for i := 0; i < MaxEventsPerBatch+10; i++ {
		server.respondOK()
	}
	for i := 0; i < MaxEventsPerBatch+10; i++ {
		tr.enqueue(WireEvent{Message: "m"})
	}
	batches := server.batches()
	firstEvents, _ := batches[0]["events"].([]any)
	if len(firstEvents) > MaxEventsPerBatch {
		t.Fatalf("first batch carried %d events, want at most %d", len(firstEvents), MaxEventsPerBatch)
	}
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}
