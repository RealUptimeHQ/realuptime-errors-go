package realuptimeerrors

import (
	"context"
	"log/slog"
)

// SlogHandler wraps next and reports every record at level >= reportLevel
// (typically slog.LevelError) to c as a message, tagged with every
// slog attribute as a Errors tag (values pass the same client-side scrub as
// everything else). Every record is still passed through to next
// unchanged: this is a hook, not a replacement handler, so existing log
// output is unaffected.
//
//	logger := slog.New(realuptimeerrors.SlogHandler(client, slog.LevelError, slog.NewJSONHandler(os.Stderr, nil)))
func SlogHandler(c *Client, reportLevel slog.Level, next slog.Handler) slog.Handler {
	return &slogHandler{client: c, level: reportLevel, next: next}
}

type slogHandler struct {
	client *Client
	level  slog.Level
	next   slog.Handler
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= h.level {
		func() {
			defer func() { _ = recover() }()
			tags := make(map[string]string, record.NumAttrs())
			record.Attrs(func(a slog.Attr) bool {
				tags[a.Key] = a.Value.String()
				return true
			})
			client := h.client
			if client == nil {
				client = FromContext(ctx)
			}
			client.CaptureMessage(record.Message, WithTags(tags))
		}()
	}
	return h.next.Handle(ctx, record)
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &slogHandler{client: h.client, level: h.level, next: h.next.WithAttrs(attrs)}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	return &slogHandler{client: h.client, level: h.level, next: h.next.WithGroup(name)}
}
