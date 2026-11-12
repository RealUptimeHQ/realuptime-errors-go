package realuptimeerrors

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Backoff and timeout constants, mirrored from errors-js/client.ts
// and pinned by wire_contract_test.go.
const (
	sendTimeout  = 10 * time.Second
	backoffStart = 5 * time.Second
	backoffMax   = 300 * time.Second
)

const overQuotaMessage = "monthly event quota reached; pausing until the window resets. " +
	"Dropped events are counted and shown on your realuptime Errors dashboard."

// httpDoer is the seam tests use to fake the network. *http.Client
// satisfies it.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type transportOptions struct {
	background bool
	httpClient httpDoer
	now        func() time.Time
	log        func(string)
}

// transport is batched, buffered, backed-off delivery. Mirrors
// errors-js/client.ts and the Ruby SDK's Transport: over-quota
// pauses until the reset instant, a revoked key disables delivery for the
// process lifetime, transient failures back off exponentially (5s doubling
// to 300s), and client-side drops ride the next successful batch as
// droppedClient so they reach the product's visible drop counters. Nothing
// here ever panics out to the host program.
//
// Delivery runs on at most one background goroutine, started lazily and
// woken by enqueue and by its own backoff timer. flush is synchronous and
// is what Client.Flush and Client.Close call.
type transport struct {
	endpoint   string
	client     httpDoer
	now        func() time.Time
	logFn      func(string)
	background bool

	mu                 sync.Mutex
	events             []WireEvent
	droppedSinceReport int
	backoff            time.Duration
	nextAttemptAt      time.Time
	pausedUntil        time.Time
	disabled           bool
	loggedKinds        map[string]bool
	closing            bool
	workerRunning      bool
	wake               chan struct{}
}

func newTransport(endpoint string, opts transportOptions) *transport {
	client := opts.httpClient
	if client == nil {
		client = &http.Client{Timeout: sendTimeout}
	}
	nowFn := opts.now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &transport{
		endpoint:    endpoint,
		client:      client,
		now:         nowFn,
		logFn:       opts.log,
		background:  opts.background,
		loggedKinds: map[string]bool{},
		wake:        make(chan struct{}, 1),
	}
}

func (t *transport) logOnce(kind, message string) {
	t.mu.Lock()
	already := t.loggedKinds[kind]
	if !already {
		t.loggedKinds[kind] = true
	}
	t.mu.Unlock()
	if !already {
		safeLog(t.logFn, message)
	}
}

func (t *transport) bufferedCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.events)
}

func (t *transport) isDisabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disabled
}

func (t *transport) enqueue(event WireEvent) {
	t.mu.Lock()
	if t.disabled {
		t.mu.Unlock()
		return
	}
	if len(t.events) >= BufferMax {
		t.events = t.events[1:]
		t.droppedSinceReport++
	}
	t.events = append(t.events, event)
	t.mu.Unlock()

	if t.background {
		t.ensureWorker()
		select {
		case t.wake <- struct{}{}:
		default:
		}
	} else {
		t.flush()
	}
}

func (t *transport) ensureWorker() {
	t.mu.Lock()
	if t.workerRunning {
		t.mu.Unlock()
		return
	}
	t.workerRunning = true
	t.closing = false
	t.mu.Unlock()
	go t.workerLoop()
}

func (t *transport) workerLoop() {
	defer func() {
		_ = recover()
		t.mu.Lock()
		t.workerRunning = false
		t.mu.Unlock()
	}()
	const idlePoll = time.Second
	for {
		t.mu.Lock()
		if t.closing {
			t.mu.Unlock()
			return
		}
		hasEvents := len(t.events) > 0
		wait := idlePoll
		if hasEvents {
			now := t.now()
			due := t.nextAttemptAt
			if t.pausedUntil.After(due) {
				due = t.pausedUntil
			}
			if due.After(now) {
				if d := due.Sub(now); d < idlePoll {
					wait = d
				}
			} else {
				wait = 0
			}
		}
		t.mu.Unlock()

		if wait > 0 {
			select {
			case <-t.wake:
			case <-time.After(wait):
			}
		}

		t.mu.Lock()
		closing := t.closing
		t.mu.Unlock()
		if closing {
			return
		}
		t.flush()
	}
}

// flush sends what is due, synchronously on the calling goroutine. Never
// panics.
func (t *transport) flush() {
	defer func() {
		if r := recover(); r != nil {
			t.logOnce("internal", "delivery failed internally (recovered panic).")
		}
	}()
	t.deliver()
}

// close stops the background worker. Safe to call more than once and on an
// inert transport.
func (t *transport) close() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *transport) deliver() {
	for {
		t.mu.Lock()
		now := t.now()
		if t.disabled || len(t.events) == 0 || now.Before(t.nextAttemptAt) || now.Before(t.pausedUntil) {
			t.mu.Unlock()
			return
		}
		n := len(t.events)
		if n > MaxEventsPerBatch {
			n = MaxEventsPerBatch
		}
		events := append([]WireEvent(nil), t.events[:n]...)
		droppedClient := t.droppedSinceReport
		t.droppedSinceReport = 0
		t.mu.Unlock()

		batch := WireBatch{SDK: SDKName + "/" + SDKVersion, DroppedClient: droppedClient, Events: events}
		body, err := json.Marshal(batch)
		if err != nil {
			// Should not happen (the batch is our own well-typed struct);
			// treat as an internal error and drop rather than loop forever.
			t.logOnce("internal", "failed to encode a batch; dropping it: "+err.Error())
			t.mu.Lock()
			t.events = t.events[len(events):]
			t.mu.Unlock()
			continue
		}

		status, respBody, doErr := t.post(body)
		if doErr != nil {
			t.mu.Lock()
			t.droppedSinceReport += droppedClient
			t.mu.Unlock()
			t.backOff("network", "cannot reach the ingest endpoint; buffering and retrying")
			return
		}

		var parsed map[string]any
		if len(respBody) > 0 {
			_ = json.Unmarshal(respBody, &parsed)
		}

		if status >= 200 && status < 300 {
			t.mu.Lock()
			t.events = t.events[len(events):]
			t.backoff = 0
			t.nextAttemptAt = time.Time{}
			t.mu.Unlock()
			if overQuota, _ := parsed["overQuota"].(bool); overQuota {
				var resetsAt string
				if quota, ok := parsed["quota"].(map[string]any); ok {
					resetsAt, _ = quota["resetsAt"].(string)
				}
				t.pauseUntil(resetsAt)
				t.logOnce("over-quota", overQuotaMessage)
				return
			}
			continue
		}

		reason, _ := parsed["reason"].(string)
		if status == 403 && reason == "key_revoked" {
			t.mu.Lock()
			t.disabled = true
			t.events = nil
			t.mu.Unlock()
			t.logOnce("revoked", "this project's ingest key was revoked; error reporting is disabled for this process. "+
				"Rotate the key in realuptime Errors settings and redeploy with the new DSN.")
			return
		}
		if status == 429 && reason == "over_quota" {
			t.mu.Lock()
			t.events = t.events[len(events):]
			t.mu.Unlock()
			resetsAt, _ := parsed["resetsAt"].(string)
			t.pauseUntil(resetsAt)
			t.logOnce("over-quota", overQuotaMessage)
			return
		}
		if status == 400 {
			t.mu.Lock()
			t.events = t.events[len(events):]
			t.droppedSinceReport += droppedClient
			t.mu.Unlock()
			detail, _ := parsed["error"].(string)
			if detail == "" {
				detail = "no detail"
			}
			t.logOnce("malformed", "the server refused a batch as malformed: "+detail+". This is an SDK bug worth reporting.")
			continue
		}

		t.mu.Lock()
		t.droppedSinceReport += droppedClient
		t.mu.Unlock()
		t.backOff("transient", "ingest endpoint answered an unexpected status; buffering and retrying")
		return
	}
}

func (t *transport) post(body []byte) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

func (t *transport) pauseUntil(resetsAt string) {
	var when time.Time
	if resetsAt != "" {
		if parsed, err := time.Parse(time.RFC3339, resetsAt); err == nil {
			when = parsed
		}
	}
	if when.IsZero() {
		when = t.now().Add(time.Hour)
	}
	t.mu.Lock()
	t.pausedUntil = when
	t.mu.Unlock()
}

func (t *transport) backOff(kind, message string) {
	t.mu.Lock()
	if t.backoff <= 0 {
		t.backoff = backoffStart
	} else {
		t.backoff *= 2
		if t.backoff > backoffMax {
			t.backoff = backoffMax
		}
	}
	t.nextAttemptAt = t.now().Add(t.backoff)
	t.mu.Unlock()
	t.logOnce(kind, message+" (backing off).")
}
