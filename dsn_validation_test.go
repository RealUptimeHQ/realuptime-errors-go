package realuptimeerrors

import (
	"strings"
	"testing"
)

// REA-575: NewClient must reject a DSN that is not the full ingest URL
// (https://<host>/api/errors/v1/ingest/<key>). Mirrors
// errors-js/dsn-validation.test.ts and
// errors-py/tests/test_dsn_validation.py case for case.

func TestNewClientRejectsBareIngestKey(t *testing.T) {
	var logs []string
	c := NewClient(Config{DSN: "rue_0123456789abcdef0123456789abcdef", Logger: func(m string) { logs = append(logs, m) }})
	if c.IsEnabled() {
		t.Fatal("expected a bare key to be rejected")
	}
	if len(logs) != 1 {
		t.Fatalf("expected exactly one log line, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "DSN must be the full ingest URL, got a bare key") {
		t.Fatalf("log line did not name the problem: %q", logs[0])
	}
	// Inert: capturing after a rejected DSN never panics.
	c.CaptureMessage("should not be reported")
}

func TestNewClientRejectsUrlNotShapedLikeIngestRoute(t *testing.T) {
	c := NewClient(Config{DSN: "https://realuptime.io/some/other/path", Logger: func(string) {}})
	if c.IsEnabled() {
		t.Fatal("expected a non-ingest-shaped URL to be rejected")
	}
}

func TestNewClientRejectsUnparseableString(t *testing.T) {
	c := NewClient(Config{DSN: "://not a url", Logger: func(string) {}})
	if c.IsEnabled() {
		t.Fatal("expected an unparseable DSN to be rejected")
	}
}

func TestNewClientRejectsNonHttpScheme(t *testing.T) {
	c := NewClient(Config{DSN: "ftp://realuptime.io/api/errors/v1/ingest/rue_abc", Logger: func(string) {}})
	if c.IsEnabled() {
		t.Fatal("expected a non-http(s) scheme to be rejected")
	}
}

func TestNewClientAcceptsFullIngestUrl(t *testing.T) {
	var logs []string
	c := NewClient(Config{
		DSN:    "https://realuptime.io/api/errors/v1/ingest/rue_0123456789abcdef0123456789abcdef",
		Logger: func(m string) { logs = append(logs, m) },
	})
	if !c.IsEnabled() {
		t.Fatal("expected a full ingest URL to be accepted")
	}
	if len(logs) != 0 {
		t.Fatalf("expected no log lines on a valid DSN, got: %v", logs)
	}
}

func TestNewClientAcceptsTrailingSlash(t *testing.T) {
	c := NewClient(Config{DSN: "https://realuptime.io/api/errors/v1/ingest/rue_abc/", Logger: func(string) {}})
	if !c.IsEnabled() {
		t.Fatal("expected a trailing slash on the ingest path to still be accepted")
	}
}

func TestClientIsEnabledFalseOnZeroValue(t *testing.T) {
	var c *Client
	if c.IsEnabled() {
		t.Fatal("expected a nil client to report disabled")
	}
}
