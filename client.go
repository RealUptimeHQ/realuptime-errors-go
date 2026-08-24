package realuptimeerrors

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Config configures a Client. Only DSN is required; everything else has an
// honest default. Never causes NewClient to panic.
type Config struct {
	// DSN is the project's ingest URL, e.g.
	// "https://realuptime.io/api/errors/v1/ingest/rue_...". A missing or
	// empty DSN produces an inert Client: every public method becomes a
	// no-op rather than an error, because a customer's error tracker must
	// never be the reason their own program cannot start.
	DSN string
	// Release, e.g. a git SHA or version tag.
	Release string
	// Environment, e.g. "production".
	Environment string
	// AllowFields opts specific fields back into the wire, by exact name:
	// lowercased header names ("x-request-id") and the dotted identity
	// names "user.email" / "user.username". Never a global switch.
	AllowFields []string
	// SendDeviceInfo attaches runtime/platform facts about this process
	// (never hostname or IP). Defaults to true.
	SendDeviceInfo *bool
	// Background delivers on a goroutine (default true); false delivers
	// inline on every capture, which is useful for short-lived CLI
	// programs and tests.
	Background *bool
	// Logger receives the SDK's own diagnostic lines (prefixed
	// "[realuptime-errors]"), logged at most once per kind. Defaults to
	// log.Println.
	Logger func(string)

	// httpClient, now and endpoint override transport internals; test-only
	// seams, unexported so they cannot be part of the public API surface.
	httpClient httpDoer
	now        func() time.Time
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Client is a RealUptime Errors client. The zero value is not usable; build
// one with NewClient. A Client is safe for concurrent use.
//
// Every public method never panics: a broken SDK logs once (via
// Config.Logger) and goes quiet.
type Client struct {
	dsn         string
	release     string
	environment string
	allowFields []string
	device      *WireDevice
	transport   *transport
	logFn       func(string)
	now         func() time.Time

	mu                 sync.Mutex
	breadcrumbs        []WireBreadcrumb
	breadcrumbsEvicted int
	user               *WireUser
	tags               map[string]string
	context            map[string]string
}

// NewClient builds a Client. It never returns nil and never panics: a
// missing DSN logs once and yields an inert client whose public methods are
// no-ops. Call Close when done to stop the background delivery goroutine
// (or rely on process exit).
func NewClient(cfg Config) *Client {
	logFn := cfg.Logger
	if logFn == nil {
		logFn = func(msg string) { log.Println(msg) }
	}
	nowFn := cfg.now
	if nowFn == nil {
		nowFn = time.Now
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		safeLog(logFn, "init called without a DSN; error reporting is disabled.")
		return &Client{logFn: logFn, now: nowFn}
	}
	c := &Client{
		dsn:         cfg.DSN,
		release:     cfg.Release,
		environment: cfg.Environment,
		allowFields: append([]string(nil), cfg.AllowFields...),
		logFn:       logFn,
		now:         nowFn,
		tags:        map[string]string{},
		context:     map[string]string{},
	}
	if boolOr(cfg.SendDeviceInfo, true) {
		c.device = detectDevice()
	}
	c.transport = newTransport(cfg.DSN, transportOptions{
		background: boolOr(cfg.Background, true),
		httpClient: cfg.httpClient,
		now:        nowFn,
		log:        logFn,
	})
	return c
}

func safeLog(logFn func(string), message string) {
	defer func() { _ = recover() }()
	if logFn != nil {
		logFn("[realuptime-errors] " + message)
	}
}

func (c *Client) log(message string) { safeLog(c.logFn, message) }

// initialized reports whether the client has a live transport (a DSN was
// given). A zero/inert client is safe to call every other method on; they
// simply do nothing.
func (c *Client) initialized() bool { return c != nil && c.transport != nil }

// EventOption customizes one captured event, layered OVER the client's
// sticky scope (SetUser/SetTag/SetContext).
type EventOption func(*eventOptions)

type eventOptions struct {
	request     *WireRequest
	fingerprint []string
	release     *string
	environment *string
	user        *WireUser
	tags        map[string]string
	context     map[string]string
}

// WithRequest attaches minimal request context to one event.
func WithRequest(r WireRequest) EventOption {
	return func(o *eventOptions) { o.request = &r }
}

// WithFingerprint overrides automatic grouping for one event.
func WithFingerprint(parts ...string) EventOption {
	return func(o *eventOptions) { o.fingerprint = parts }
}

// WithRelease overrides the client's configured release for one event.
func WithRelease(release string) EventOption {
	return func(o *eventOptions) { o.release = &release }
}

// WithEnvironment overrides the client's configured environment for one event.
func WithEnvironment(environment string) EventOption {
	return func(o *eventOptions) { o.environment = &environment }
}

// WithUser attaches identity to one event, merged over the sticky user set
// by SetUser.
func WithUser(u WireUser) EventOption {
	return func(o *eventOptions) { o.user = &u }
}

// WithTags attaches tags to one event, merged over sticky tags.
func WithTags(tags map[string]string) EventOption {
	return func(o *eventOptions) { o.tags = tags }
}

// WithContext attaches custom context to one event, merged over sticky context.
func WithContext(context map[string]string) EventOption {
	return func(o *eventOptions) { o.context = context }
}

// CaptureException reports err. Never panics. The stack is captured at the
// call site (Go errors carry no stack of their own), innermost frame first,
// capped at MaxFramesPerEvent.
func (c *Client) CaptureException(err error, opts ...EventOption) {
	defer func() { _ = recover() }()
	if !c.initialized() || err == nil {
		return
	}
	message := err.Error()
	if message == "" {
		message = fmt.Sprintf("%T", err)
	}
	exceptionType := fmt.Sprintf("%T", err)
	frames := framesFromCallers(3)
	event := c.buildEvent(message, &exceptionType, frames, opts)
	c.transport.enqueue(scrubEvent(event, c.allowFields))
}

// CaptureMessage reports a plain message. Never panics.
func (c *Client) CaptureMessage(message string, opts ...EventOption) {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	event := c.buildEvent(message, nil, nil, opts)
	c.transport.enqueue(scrubEvent(event, c.allowFields))
}

// AddBreadcrumb records one breadcrumb onto the bounded trail (at most
// MaxBreadcrumbsPerEvent; older entries are evicted and the eviction count
// rides the next event as BreadcrumbsDropped). Never panics.
func (c *Client) AddBreadcrumb(message string, category string, data map[string]string) {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	var crumbData map[string]string
	if data != nil {
		crumbData = map[string]string{}
		n := 0
		for k, v := range data {
			if n >= MaxBreadcrumbDataEntries {
				break
			}
			crumbData[clip(k, MaxStringLength)] = clip(v, MaxStringLength)
			n++
		}
	}
	ts := isoTime(c.now())
	var categoryPtr *string
	if category != "" {
		cc := clip(category, 100)
		categoryPtr = &cc
	}
	crumb := WireBreadcrumb{Timestamp: &ts, Category: categoryPtr, Message: clip(message, MaxStringLength), Data: crumbData}
	c.mu.Lock()
	c.breadcrumbs = append(c.breadcrumbs, crumb)
	if len(c.breadcrumbs) > MaxBreadcrumbsPerEvent {
		c.breadcrumbs = c.breadcrumbs[1:]
		c.breadcrumbsEvicted++
	}
	c.mu.Unlock()
}

// SetUser sets sticky identity applied to every subsequent event. Passing
// the zero value clears it. Never panics.
func (c *Client) SetUser(u WireUser) {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	next := WireUser{}
	if u.ID != "" {
		next.ID = clip(u.ID, MaxStringLength)
	}
	if u.Email != "" {
		next.Email = clip(u.Email, MaxStringLength)
	}
	if u.Username != "" {
		next.Username = clip(u.Username, MaxStringLength)
	}
	c.mu.Lock()
	if next == (WireUser{}) {
		c.user = nil
	} else {
		c.user = &next
	}
	c.mu.Unlock()
}

// SetTag sets one sticky tag; an empty value removes it. Tags past
// MaxTagsPerEvent are ignored rather than evicting an existing one. Never
// panics.
func (c *Client) SetTag(key, value string) {
	defer func() { _ = recover() }()
	if !c.initialized() || key == "" {
		return
	}
	name := clip(key, MaxContextKeyLength)
	c.mu.Lock()
	defer c.mu.Unlock()
	if value == "" {
		delete(c.tags, name)
		return
	}
	if _, exists := c.tags[name]; !exists && len(c.tags) >= MaxTagsPerEvent {
		return
	}
	c.tags[name] = clip(value, MaxStringLength)
}

// SetTags sets several sticky tags at once.
func (c *Client) SetTags(tags map[string]string) {
	for k, v := range tags {
		c.SetTag(k, v)
	}
}

// SetContext sets one sticky custom-context entry; an empty value removes
// it. Never panics.
func (c *Client) SetContext(key, value string) {
	defer func() { _ = recover() }()
	if !c.initialized() || key == "" {
		return
	}
	name := clip(key, MaxContextKeyLength)
	c.mu.Lock()
	defer c.mu.Unlock()
	if value == "" {
		delete(c.context, name)
		return
	}
	if _, exists := c.context[name]; !exists && len(c.context) >= MaxContextEntries {
		return
	}
	c.context[name] = clip(value, MaxStringLength)
}

// ClearContext removes every sticky custom-context entry.
func (c *Client) ClearContext() {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	c.mu.Lock()
	c.context = map[string]string{}
	c.mu.Unlock()
}

// Flush delivers anything buffered, synchronously. Never panics.
func (c *Client) Flush() {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	c.transport.flush()
}

// Close stops the background delivery goroutine. Safe to call on an inert
// client. Never panics.
func (c *Client) Close() {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	c.transport.close()
}

// Recover reports a recovered panic (call from inside a deferred
// recover()), then re-panics with the original value so the caller's own
// process-death semantics are unchanged. Pass the value recover() returned;
// a nil value is a no-op.
func (c *Client) Recover(recovered any, opts ...EventOption) {
	if recovered == nil {
		return
	}
	c.captureRecovered(recovered, opts)
	panic(recovered)
}

// RecoverWithoutRePanic reports a recovered panic and swallows it (does not
// re-panic). Useful for a supervisor goroutine that wants to keep running.
func (c *Client) RecoverWithoutRePanic(recovered any, opts ...EventOption) {
	if recovered == nil {
		return
	}
	c.captureRecovered(recovered, opts)
}

func (c *Client) captureRecovered(recovered any, opts []EventOption) {
	defer func() { _ = recover() }()
	if !c.initialized() {
		return
	}
	message := fmt.Sprintf("%v", recovered)
	exceptionType := "panic"
	if err, ok := recovered.(error); ok {
		exceptionType = fmt.Sprintf("%T", err)
	}
	frames := framesFromCallers(4)
	event := c.buildEvent(message, &exceptionType, frames, opts)
	c.transport.enqueue(scrubEvent(event, c.allowFields))
}

// buildEvent assembles one wire event (v1 fields always present; v2 fields
// only when non-empty, so a client that never touches the v2 API keeps
// producing byte-identical v1 payloads).
func (c *Client) buildEvent(message string, exceptionType *string, frames []WireFrame, opts []EventOption) WireEvent {
	var options eventOptions
	for _, opt := range opts {
		opt(&options)
	}

	c.mu.Lock()
	breadcrumbs := append([]WireBreadcrumb(nil), c.breadcrumbs...)
	breadcrumbsDropped := c.breadcrumbsEvicted
	scopeUser := c.user
	scopeTags := cloneMap(c.tags)
	scopeContext := cloneMap(c.context)
	c.mu.Unlock()

	release := c.release
	if options.release != nil {
		release = *options.release
	}
	environment := c.environment
	if options.environment != nil {
		environment = *options.environment
	}

	var releasePtr, envPtr *string
	if release != "" {
		releasePtr = &release
	}
	if environment != "" {
		envPtr = &environment
	}

	event := WireEvent{
		OccurredAt:         isoTime(c.now()),
		Message:            clip(message, MaxMessageLength),
		ExceptionType:      exceptionType,
		Release:            releasePtr,
		Environment:        envPtr,
		Frames:             frames,
		Request:            options.request,
		Fingerprint:        options.fingerprint,
		Breadcrumbs:        emptyToNilBreadcrumbs(breadcrumbs),
		BreadcrumbsDropped: breadcrumbsDropped,
		Device:             c.device,
	}

	mergedUser := scopeUser
	if options.user != nil {
		base := WireUser{}
		if scopeUser != nil {
			base = *scopeUser
		}
		if options.user.ID != "" {
			base.ID = clip(options.user.ID, MaxStringLength)
		}
		if options.user.Email != "" {
			base.Email = clip(options.user.Email, MaxStringLength)
		}
		if options.user.Username != "" {
			base.Username = clip(options.user.Username, MaxStringLength)
		}
		mergedUser = &base
	}
	if mergedUser != nil && *mergedUser != (WireUser{}) {
		event.User = mergedUser
	}

	mergedTags := mergeBounded(scopeTags, options.tags, MaxTagsPerEvent, MaxContextKeyLength)
	if len(mergedTags) > 0 {
		event.Tags = mergedTags
	}
	mergedContext := mergeBounded(scopeContext, options.context, MaxContextEntries, MaxContextKeyLength)
	if len(mergedContext) > 0 {
		event.Context = mergedContext
	}
	return event
}

func emptyToNilBreadcrumbs(b []WireBreadcrumb) []WireBreadcrumb {
	if len(b) == 0 {
		return nil
	}
	return b
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mergeBounded merges extra (a per-call override) over base (the sticky
// scope), bounded at limit entries total, dropping past-limit extras rather
// than evicting an existing entry.
func mergeBounded(base, extra map[string]string, limit, keyLimit int) map[string]string {
	out := cloneMap(base)
	for k, v := range extra {
		if v == "" || k == "" {
			continue
		}
		name := clip(k, keyLimit)
		if _, exists := out[name]; !exists && len(out) >= limit {
			continue
		}
		out[name] = clip(v, MaxStringLength)
	}
	return out
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// detectDevice reads runtime/platform facts out of this process. Never the
// hostname, the local IP or a container id.
func detectDevice() *WireDevice {
	return &WireDevice{
		Runtime:        "go",
		RuntimeVersion: strings.TrimPrefix(runtime.Version(), "go"),
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
}

// framesFromCallers walks the current goroutine's stack, innermost first,
// capped at MaxFramesPerEvent. skip is the number of runtime.Callers frames
// to discard (this function, its caller, and the public entry point).
// inApp is a best-effort guess: not inside GOROOT and not inside this SDK's
// own package.
func framesFromCallers(skip int) []WireFrame {
	pcs := make([]uintptr, MaxFramesPerEvent+skip)
	n := runtime.Callers(skip, pcs)
	if n == 0 {
		return nil
	}
	frameIter := runtime.CallersFrames(pcs[:n])
	frames := make([]WireFrame, 0, n)
	for {
		f, more := frameIter.Next()
		if f.Function != "" || f.File != "" {
			file := f.File
			line := f.Line
			function := f.Function
			frame := WireFrame{
				File:  clip(file, MaxStringLength),
				Line:  &line,
				InApp: inApp(file, function),
			}
			if function != "" {
				fn := clip(function, MaxStringLength)
				frame.Function = &fn
			}
			frames = append(frames, frame)
		}
		if !more || len(frames) >= MaxFramesPerEvent {
			break
		}
	}
	return frames
}

func inApp(file, function string) bool {
	if file == "" {
		return false
	}
	goroot := runtime.GOROOT()
	if goroot != "" && strings.HasPrefix(file, goroot) {
		return false
	}
	if strings.Contains(file, string(filepath.Separator)+"errors-go"+string(filepath.Separator)) &&
		strings.HasPrefix(function, "github.com/RealUptimeHQ/realuptime-errors-go.") {
		return false
	}
	if strings.HasPrefix(function, "runtime.") {
		return false
	}
	return true
}
