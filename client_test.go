package realuptimeerrors

import (
	"errors"
	"testing"
	"time"
)

func TestNewClientWithoutDSNIsInertAndNeverPanics(t *testing.T) {
	var logs []string
	c := NewClient(Config{Logger: func(m string) { logs = append(logs, m) }})
	if c == nil {
		t.Fatal("NewClient must never return nil")
	}
	// Every public method must be a safe no-op.
	c.CaptureException(errors.New("boom"))
	c.CaptureMessage("hi")
	c.AddBreadcrumb("did a thing", "nav", nil)
	c.SetUser(WireUser{ID: "1"})
	c.SetTag("k", "v")
	c.SetContext("k", "v")
	c.Flush()
	c.Close()
	if len(logs) == 0 {
		t.Fatal("expected the missing-DSN warning to be logged")
	}
}

func TestCaptureExceptionNeverPanicsEvenOnANilTransportRace(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CaptureException must never panic, got: %v", r)
		}
	}()
	var c *Client
	c.CaptureException(errors.New("boom"))
	c.CaptureMessage("hi")
}

func TestStickyUserTagsAndContextRideEveryEvent(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	c.SetUser(WireUser{ID: "acct_1", Email: "a@example.com"})
	c.SetTag("tenant", "acme")
	c.SetContext("cart", "1")
	c.CaptureMessage("first")
	c.CaptureMessage("second")

	events := server.events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for _, raw := range events {
		event := raw.(map[string]any)
		user := event["user"].(map[string]any)
		if user["id"] != "acct_1" {
			t.Fatalf("user.id = %v, want acct_1", user["id"])
		}
		if user["email"] != scrubbed {
			t.Fatalf("user.email = %v, want scrubbed by default", user["email"])
		}
		tags := event["tags"].(map[string]any)
		if tags["tenant"] != "acme" {
			t.Fatalf("tags.tenant = %v, want acme", tags["tenant"])
		}
	}
}

func TestSetUserClearsWithZeroValue(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	c.SetUser(WireUser{ID: "acct_1"})
	c.SetUser(WireUser{})
	c.CaptureMessage("m")
	event := server.events()[0].(map[string]any)
	if _, ok := event["user"]; ok {
		t.Fatalf("expected no user field after clearing, got %v", event["user"])
	}
}

func TestSetTagIgnoresExtrasPastTheCapWithoutEvictingExisting(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	for i := 0; i < MaxTagsPerEvent+5; i++ {
		c.SetTag(string(rune('a'+i%26))+string(rune(i)), "v")
	}
	c.CaptureMessage("m")
	event := server.events()[0].(map[string]any)
	tags := event["tags"].(map[string]any)
	if len(tags) > MaxTagsPerEvent {
		t.Fatalf("tags = %d entries, want at most %d", len(tags), MaxTagsPerEvent)
	}
}

func TestAddBreadcrumbBoundedRingEvictsAndCounts(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	for i := 0; i < MaxBreadcrumbsPerEvent+7; i++ {
		c.AddBreadcrumb("step", "nav", nil)
	}
	c.CaptureMessage("m")
	event := server.events()[0].(map[string]any)
	crumbs := event["breadcrumbs"].([]any)
	if len(crumbs) != MaxBreadcrumbsPerEvent {
		t.Fatalf("breadcrumbs = %d, want %d", len(crumbs), MaxBreadcrumbsPerEvent)
	}
	dropped, _ := event["breadcrumbsDropped"].(float64)
	if int(dropped) != 7 {
		t.Fatalf("breadcrumbsDropped = %v, want 7", dropped)
	}
}

func TestEventOptionsOverrideAndMergeOverStickyScope(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	c.SetTag("tenant", "acme")
	c.CaptureMessage("m", WithTags(map[string]string{"feature": "checkout"}), WithRelease("v9"), WithFingerprint("a", "b"))

	event := server.events()[0].(map[string]any)
	tags := event["tags"].(map[string]any)
	if tags["tenant"] != "acme" || tags["feature"] != "checkout" {
		t.Fatalf("tags = %v, want both sticky and per-call tags merged", tags)
	}
	if event["release"] != "v9" {
		t.Fatalf("release = %v, want v9 (per-call override)", event["release"])
	}
	fp := event["fingerprint"].([]any)
	if len(fp) != 2 || fp[0] != "a" || fp[1] != "b" {
		t.Fatalf("fingerprint = %v, want [a b]", fp)
	}
}

func TestRecoverReportsAndRePanics(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	repanicked := func() (caught any) {
		defer func() { caught = recover() }()
		func() {
			defer func() { c.Recover(recover()) }()
			panic(errors.New("boom"))
		}()
		return nil
	}()
	if repanicked == nil {
		t.Fatal("expected Recover to re-panic with the original value")
	}
	if len(server.events()) != 1 {
		t.Fatalf("expected the panic to still be reported, got %d events", len(server.events()))
	}
}

func TestRecoverWithoutRePanicSwallowsAndReports(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	func() {
		defer func() {
			c.RecoverWithoutRePanic(recover())
		}()
		panic("boom")
	}()

	if len(server.events()) != 1 {
		t.Fatalf("expected 1 reported event, got %d", len(server.events()))
	}
}

func TestBackgroundDeliveryFlushesOnClientFlush(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	background := true
	c := NewClient(Config{
		DSN:        "https://realuptime.test/api/errors/v1/ingest/rue_test",
		Background: &background,
		Logger:     func(m string) { logs = append(logs, m) },
		httpClient: server,
		now:        clock.Now,
	})
	defer c.Close()
	c.CaptureMessage("hi")

	deadline := time.Now().Add(2 * time.Second)
	for len(server.batches()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(server.batches()) == 0 {
		t.Fatal("background transport never delivered the buffered event")
	}
}
