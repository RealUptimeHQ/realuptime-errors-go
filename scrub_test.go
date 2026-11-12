package realuptimeerrors

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Drives the SHARED scrub vectors (scrub-vectors.json, vendored from
// errors-js) through this SDK's scrubEvent, the
// errors-js/scrub.test.ts convention ported to Go. This test must
// never grow expectations of its own: a new vector in the shared file is
// what every SDK's suite picks up automatically.
//
// Vectors are loose JSON (Partial<WireEvent> on the JS side), so this test
// merges each vector's "event"/"expected" patch over the same base v1
// defaults the JS test uses, round-trips through WireEvent (so the merge
// hits real Go types, catching a field-name mismatch the same way the wire
// contract test does), and compares the JSON-normalized results.

type vectorsFile struct {
	Vectors []struct {
		Name        string         `json:"name"`
		AllowFields []string       `json:"allowFields"`
		Event       map[string]any `json:"event"`
		Expected    map[string]any `json:"expected"`
	} `json:"vectors"`
}

func baseEventMap() map[string]any {
	return map[string]any{
		"occurredAt":         "2026-08-21T00:00:00.000Z",
		"message":            "",
		"exceptionType":      nil,
		"release":            nil,
		"environment":        nil,
		"frames":             nil,
		"request":            nil,
		"fingerprint":        nil,
		"breadcrumbs":        nil,
		"breadcrumbsDropped": float64(0),
	}
}

func mergeMap(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	return out
}

func toNormalizedMap(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestScrubVectors(t *testing.T) {
	var vf vectorsFile
	if err := json.Unmarshal(vendoredScrubVectors, &vf); err != nil {
		t.Fatalf("parsing scrub-vectors.json: %v", err)
	}
	if len(vf.Vectors) < 15 {
		t.Fatalf("expected at least 15 shared vectors, got %d", len(vf.Vectors))
	}

	for _, vec := range vf.Vectors {
		vec := vec
		t.Run(vec.Name, func(t *testing.T) {
			inputMap := mergeMap(baseEventMap(), vec.Event)
			inputJSON, err := json.Marshal(inputMap)
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			var event WireEvent
			if err := json.Unmarshal(inputJSON, &event); err != nil {
				t.Fatalf("unmarshal into WireEvent: %v", err)
			}

			result := scrubEvent(event, vec.AllowFields)
			resultJSON, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			var resultMap map[string]any
			if err := json.Unmarshal(resultJSON, &resultMap); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			expectedMap := toNormalizedMap(t, mergeMap(baseEventMap(), vec.Expected))

			if !reflect.DeepEqual(resultMap, expectedMap) {
				t.Fatalf("vector %q:\n got:  %s\n want: %s", vec.Name, mustJSON(resultMap), mustJSON(expectedMap))
			}
		})
	}
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// The input event must never be mutated by scrubEvent; a caller may still
// be holding the same value inside an exception handler.
func TestScrubEventDoesNotMutateInput(t *testing.T) {
	headers := map[string]string{"cookie": "sid=1"}
	req := WireRequest{Method: "GET", Path: "/x", Headers: headers}
	event := WireEvent{Message: "sk_live_abcdefgh12345678", Request: &req}

	_ = scrubEvent(event, nil)

	if event.Message != "sk_live_abcdefgh12345678" {
		t.Fatalf("input message was mutated: %q", event.Message)
	}
	if event.Request.Headers["cookie"] != "sid=1" {
		t.Fatalf("input request headers were mutated: %v", event.Request.Headers)
	}
}

// Error messages often carry user input. Go's RE2 is linear already; this
// pins the pattern shared with the other SDKs to stay that way.
func TestScrubStringHostileInputIsFast(t *testing.T) {
	hostile := strings.Repeat("a.", 300) + "://" + strings.Repeat(":", 2000)
	started := time.Now()
	scrubString(hostile)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("scrubString took %v on hostile input", elapsed)
	}
}
