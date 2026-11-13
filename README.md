# realuptime-errors-go

Zero-dependency (standard library only) Go error tracking SDK: part of
[RealUptime Errors](https://realuptime.io/error-tracking).

This is a **mirror**. It is published from `errors-go` in the
[realuptime monorepo](https://github.com/RealUptimeHQ/realuptime) by
`scripts/publish-sdk-mirrors.mjs` and is not edited directly; open issues
and PRs against this repo, but expect source changes to land here after
they merge upstream.

## Install

Installs straight from GitHub with plain `go get`; a Go module needs no separate registry publish step, so this is the permanent install command, not a placeholder.

```bash
go get github.com/RealUptimeHQ/realuptime-errors-go
```

## Usage

```go
import realuptimeerrors "github.com/RealUptimeHQ/realuptime-errors-go"

client := realuptimeerrors.NewClient(realuptimeerrors.Config{
    DSN:         "https://realuptime.io/api/errors/v1/ingest/rue_...", // from your project's dashboard
    Release:     "v2.4.1",
    Environment: "production",
})
defer client.Close()

client.CaptureException(err)
```

net/http: `handler := realuptimeerrors.Middleware(client)(mux)` -- recovers a
panic escaping the handler below it, reports it with minimal request
context, answers with a 500, and does not re-panic.

log/slog: `slog.New(realuptimeerrors.SlogHandler(client, slog.LevelError, base))`
reports every record at or above the given level and still passes every
record through to the wrapped handler.

## What this SDK actually does

- **Never panics out of a public method.** Every entry point recovers
  internally; a broken SDK logs once (prefixed `[realuptime-errors]`) and
  goes quiet. An error tracker that crashes the program it watches is worse
  than none.
- **Scrubs PII by default, client-side**, before anything is serialized:
  `Authorization`/`Proxy-Authorization`/`Cookie`/`Set-Cookie` headers are
  replaced whole; card-shaped digit runs (Luhn-checked), JWTs, prefixed API
  keys, and long hex/base64 runs are pattern-scrubbed anywhere they appear
  in the message, request context, breadcrumbs, tags and context values.
  `user.email` and `user.username` are removed unless opted back in by
  exact name with `Config.AllowFields`; there is no global "disable
  scrubbing" switch, by design. The vectors in `scrub-vectors.json` are
  the same file the JS and Python SDKs and the server are tested against.
- **Never silently drops.** The in-memory buffer (200 events) evicts the
  oldest event when full, counts every eviction, and reports the count on
  the next successful batch, so drops show up on your dashboard instead of
  disappearing.
- **Standard library only.** Zero `go.mod` `require` entries, checked by
  the module's own import scan.
- **Background delivery** on at most one goroutine per client, exponential
  backoff 5s to 300s, an over-quota pause until the window resets, and a
  revoked key disables delivery for the client's lifetime.
  `client.Flush()` delivers synchronously.

## API

| Function/Method | Notes |
| --- | --- |
| `NewClient(Config{DSN, Release, Environment, AllowFields, SendDeviceInfo, Background, Logger})` | Never returns nil; a missing DSN logs once and yields an inert client. |
| `(*Client) CaptureException(err error, opts ...EventOption)` | Reports an error, stack captured at the call site. |
| `(*Client) CaptureMessage(message string, opts ...EventOption)` | Reports a plain message. |
| `(*Client) SetUser` / `SetTag` / `SetTags` / `SetContext` / `ClearContext` | Sticky identity, tags and context for every later event. |
| `(*Client) AddBreadcrumb(message, category string, data map[string]string)` | Bounded trail (last 20), rides the next event. |
| `(*Client) Recover` / `RecoverWithoutRePanic` | Reports a recovered panic; the first re-panics, the second does not. |
| `(*Client) Flush` / `Close` | Deliver synchronously / stop the background goroutine. |
| `Middleware(*Client) func(http.Handler) http.Handler` | Panic recovery + request context for `net/http`. |
| `SlogHandler(*Client, slog.Level, slog.Handler) slog.Handler` | Reports records at or above the given level; passes every record through. |

## Tests

```bash
go test ./...
```

## Version

This mirror tracks SDKVersion `0.1.1` in `wire.go`, the string every
event actually carries on the wire.

## License

MIT, see [LICENSE](./LICENSE).
