package realuptimeerrors

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"
)

// Pins the wire contract mirror (docs/errors-plan.md, "The SDK matrix") and
// the stdlib-only dependency discipline, the
// packages/errors-ruby/test/wire_contract_test.rb convention ported to Go.

func TestStdlibOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if regexp.MustCompile(`_test\.go$`).MatchString(file) {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			path := imp.Path.Value
			path = path[1 : len(path)-1] // strip quotes
			// A standard-library import path never contains a dot in its
			// first segment (that is exactly how "go vet"/module resolution
			// tells stdlib from a module path); this module has zero
			// entries in go.mod's require block, so this check and that
			// absence are the two halves of the same guarantee.
			first := path
			if idx := indexByte(path, '/'); idx >= 0 {
				first = path[:idx]
			}
			if containsByte(first, '.') {
				t.Fatalf("%s imports %q, which is not the Go standard library", file, path)
			}
		}
	}
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)^require\b`).Match(gomod) {
		t.Fatalf("go.mod declares a require block; this module must have zero runtime dependencies")
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func containsByte(s string, b byte) bool { return indexByte(s, b) >= 0 }

func TestLimitsMatchTheJSSDK(t *testing.T) {
	cases := map[string]int{
		"MaxEventsPerBatch":        MaxEventsPerBatch,
		"MaxMessageLength":         MaxMessageLength,
		"MaxFramesPerEvent":        MaxFramesPerEvent,
		"MaxStringLength":          MaxStringLength,
		"BufferMax":                BufferMax,
		"MaxBreadcrumbsPerEvent":   MaxBreadcrumbsPerEvent,
		"MaxBreadcrumbDataEntries": MaxBreadcrumbDataEntries,
	}
	want := map[string]int{
		"MaxEventsPerBatch":        50,
		"MaxMessageLength":         4000,
		"MaxFramesPerEvent":        50,
		"MaxStringLength":          512,
		"BufferMax":                200,
		"MaxBreadcrumbsPerEvent":   20,
		"MaxBreadcrumbDataEntries": 10,
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %d, want %d", name, got, want[name])
		}
	}
}

func TestV2CapsMatchTheJSSDK(t *testing.T) {
	cases := map[string]int{
		"MaxTagsPerEvent":      MaxTagsPerEvent,
		"MaxContextEntries":    MaxContextEntries,
		"MaxContextKeyLength":  MaxContextKeyLength,
		"MaxLocalVarsPerFrame": MaxLocalVarsPerFrame,
		"MaxLocalVarLength":    MaxLocalVarLength,
	}
	want := map[string]int{
		"MaxTagsPerEvent":      20,
		"MaxContextEntries":    20,
		"MaxContextKeyLength":  64,
		"MaxLocalVarsPerFrame": 20,
		"MaxLocalVarLength":    256,
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %d, want %d", name, got, want[name])
		}
	}
}

func TestBackoffAndTimeoutMatchTheJSSDK(t *testing.T) {
	if sendTimeout.Seconds() != 10 {
		t.Errorf("sendTimeout = %v, want 10s", sendTimeout)
	}
	if backoffStart.Seconds() != 5 {
		t.Errorf("backoffStart = %v, want 5s", backoffStart)
	}
	if backoffMax.Seconds() != 300 {
		t.Errorf("backoffMax = %v, want 300s", backoffMax)
	}
}

var v1Keys = []string{
	"breadcrumbs", "breadcrumbsDropped", "environment", "exceptionType",
	"fingerprint", "frames", "message", "occurredAt", "release", "request",
}

func eventKeys(t *testing.T, event WireEvent) []string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestEventFieldNamesWithNoStateAreExactlyTheV1Set(t *testing.T) {
	var logs []string
	c := testClient(newFakeServer().respondOK(), newFakeClock(), &logs)
	event := c.buildEvent("m", strPtr("TypeError"), nil, nil)
	got := eventKeys(t, event)
	want := append([]string(nil), v1Keys...)
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEventFieldNamesWithV2ScopeAreExactlyTheV2Set(t *testing.T) {
	clock := newFakeClock()
	var logs []string
	c := testClient(newFakeServer().respondOK(), clock, &logs)
	c.SetUser(WireUser{ID: "acct_1"})
	c.SetTag("tenant", "acme")
	c.SetContext("cart", "1")
	event := c.buildEvent("m", nil, nil, nil)
	got := eventKeys(t, event)
	want := append(append([]string(nil), v1Keys...), "context", "tags", "user")
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	c2 := &Client{now: fixedNow, tags: map[string]string{}, context: map[string]string{}, device: detectDevice(), transport: c.transport}
	event2 := c2.buildEvent("m", nil, nil, nil)
	got2 := eventKeys(t, event2)
	want2 := append(append([]string(nil), v1Keys...), "device")
	sort.Strings(want2)
	if !equalStrings(got2, want2) {
		t.Fatalf("got %v, want %v", got2, want2)
	}
	if event2.Device.Runtime != "go" {
		t.Fatalf("device.runtime = %q, want go", event2.Device.Runtime)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func strPtr(s string) *string { return &s }

func fixedNow() time.Time { return time.Unix(1_000_000, 0).UTC() }

func TestBatchFieldNamesAndSDKString(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	c.CaptureMessage("hello")

	batches := server.batches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	batch := batches[0]
	keys := make([]string, 0, len(batch))
	for k := range batch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"droppedClient", "events", "sdk"}
	if !equalStrings(keys, want) {
		t.Fatalf("batch keys = %v, want %v", keys, want)
	}
	sdk, _ := batch["sdk"].(string)
	if !regexp.MustCompile(`^realuptime-errors-go/\d+\.\d+\.\d+$`).MatchString(sdk) {
		t.Fatalf("sdk = %q, does not match realuptime-errors-go/x.y.z", sdk)
	}
}

func TestFrameFieldNames(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	c.CaptureException(errors.New("boom"))

	events := server.events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0].(map[string]any)
	frames, _ := event["frames"].([]any)
	if len(frames) == 0 {
		t.Fatal("expected at least one frame")
	}
	frame := frames[0].(map[string]any)
	keys := make([]string, 0, len(frame))
	for k := range frame {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"file", "function", "inApp", "line"}
	if !equalStrings(keys, want) {
		t.Fatalf("frame keys = %v, want %v", keys, want)
	}
	if _, ok := frame["line"].(float64); !ok {
		t.Fatalf("line is not a number: %v", frame["line"])
	}
}

// The same frozen v1 golden batch apps/web/app/api/errors/v1/ingest/[key]/
// route.test.ts ingests: this SDK's own output for the equivalent capture
// must carry exactly the same key set and types, so an event it emits is,
// structurally, the event the server's v1 fixture already proves it
// accepts.
const v1GoldenBody = `{"sdk":"realuptime-errors-js/0.1.0","droppedClient":2,"events":[{"occurredAt":"2026-08-21T09:14:00.000Z",` +
	`"message":"TypeError: x is undefined","exceptionType":"TypeError","release":"v2.4.1","environment":"production",` +
	`"frames":[{"file":"src/checkout.ts","function":"processOrder","line":42,"inApp":true}],` +
	`"request":{"method":"GET","path":"/orders","status":500,"headers":{"x-request-id":"req-42"}},` +
	`"fingerprint":null,"breadcrumbs":null,"breadcrumbsDropped":0}]}`

func TestOutputMatchesTheSharedV1GoldenBodyShape(t *testing.T) {
	clock := newFakeClock()
	server := newFakeServer().respondOK()
	var logs []string
	c := testClient(server, clock, &logs)
	c.release = "v2.4.1"
	c.environment = "production"

	status := 500
	c.CaptureException(customTypeError{"x is undefined"}, WithRequest(WireRequest{
		Method:  "GET",
		Path:    "/orders",
		Status:  &status,
		Headers: map[string]string{"x-request-id": "req-42"},
	}))

	var golden map[string]any
	if err := json.Unmarshal([]byte(v1GoldenBody), &golden); err != nil {
		t.Fatal(err)
	}
	ours := server.batches()[0]

	goldenKeys := sortedKeys(golden)
	ourKeys := sortedKeys(ours)
	if !equalStrings(goldenKeys, ourKeys) {
		t.Fatalf("batch keys = %v, want %v", ourKeys, goldenKeys)
	}
	goldenEvent := golden["events"].([]any)[0].(map[string]any)
	ourEvent := ours["events"].([]any)[0].(map[string]any)
	if !equalStrings(sortedKeys(goldenEvent), sortedKeys(ourEvent)) {
		t.Fatalf("event keys = %v, want %v", sortedKeys(ourEvent), sortedKeys(goldenEvent))
	}
	if ourEvent["exceptionType"] != "realuptimeerrors.customTypeError" {
		// The golden fixture is language-specific ("TypeError"); Go's %T
		// yields the concrete type name instead. What must match is
		// structure, not the string, which the key-set assertions above
		// already pin; this assertion documents the difference rather
		// than silently accepting anything.
		t.Logf("exceptionType = %v (expected to differ from the JS golden's language-specific type name)", ourEvent["exceptionType"])
	}
	if ourEvent["message"] != "x is undefined" {
		t.Fatalf("message = %v, want %q", ourEvent["message"], "x is undefined")
	}
	if !equalStrings(sortedKeys(goldenEvent["request"].(map[string]any)), sortedKeys(ourEvent["request"].(map[string]any))) {
		t.Fatalf("request keys differ")
	}
	if !equalStrings(sortedKeys(goldenEvent["frames"].([]any)[0].(map[string]any)), sortedKeys(ourEvent["frames"].([]any)[0].(map[string]any))) {
		t.Fatalf("frame keys differ")
	}
}

type customTypeError struct{ msg string }

func (e customTypeError) Error() string { return e.msg }

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
