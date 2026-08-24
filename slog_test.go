package realuptimeerrors

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestSlogHandlerReportsAtOrAboveLevelAndPassesThrough(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(SlogHandler(c, slog.LevelError, base))

	logger.Info("just info, not reported")
	logger.Error("db exploded", slog.String("table", "orders"))

	events := server.events()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 reported event (the Error one), got %d", len(events))
	}
	event := events[0].(map[string]any)
	if event["message"] != "db exploded" {
		t.Fatalf("message = %v, want %q", event["message"], "db exploded")
	}
	tags := event["tags"].(map[string]any)
	if tags["table"] != "orders" {
		t.Fatalf("tags.table = %v, want orders", tags["table"])
	}
	// The underlying handler still received both records: this is a hook,
	// not a replacement.
	if !bytes.Contains(buf.Bytes(), []byte("just info")) || !bytes.Contains(buf.Bytes(), []byte("db exploded")) {
		t.Fatalf("expected both records to still reach the wrapped handler, got: %s", buf.String())
	}
}

func TestSlogHandlerFallsBackToContextClient(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)

	var buf bytes.Buffer
	logger := slog.New(SlogHandler(nil, slog.LevelError, slog.NewTextHandler(&buf, nil)))
	ctx := WithClient(context.Background(), c)
	logger.ErrorContext(ctx, "boom")

	if len(server.events()) != 1 {
		t.Fatalf("expected 1 event via the context client, got %d", len(server.events()))
	}
}
