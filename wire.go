// Package realuptimeerrors is a zero-dependency (standard library only) Go
// error tracking SDK for RealUptime Errors (docs/errors-plan.md, Linear
// REA-255). One wire contract with the JS, Python and Ruby SDKs
// (packages/errors-js/types.ts); the shared scrub vectors
// (scrub-vectors.json, vendored into this module and asserted
// byte-identical to packages/errors-js/scrub-vectors.json by
// wire_contract_test.go) pin every implementation to identical scrubbing.
//
// Contract with the host program, non-negotiable: THIS SDK NEVER PANICS out
// of a public function, and it never lets a panic escape a goroutine it
// spawned. A broken SDK logs once and goes quiet; an error tracker that
// crashes the program it watches is worse than none.
package realuptimeerrors

// Wire limits, mirrored from packages/errors-js/types.ts and pinned by
// wire_contract_test.go.
const (
	MaxEventsPerBatch        = 50
	MaxMessageLength         = 4000
	MaxFramesPerEvent        = 50
	MaxStringLength          = 512
	MaxBreadcrumbsPerEvent   = 20
	MaxBreadcrumbDataEntries = 10
	BufferMax                = 200

	// v2 caps (REA-182), mirrored from packages/errors-js/types.ts.
	MaxTagsPerEvent      = 20
	MaxContextEntries    = 20
	MaxContextKeyLength  = 64
	MaxLocalVarsPerFrame = 20
	MaxLocalVarLength    = 256
)

// SDKName and SDKVersion together form the wire batch's "sdk" string, e.g.
// "realuptime-errors-go/0.1.0". SDKVersion is also read by
// scripts/publish-sdk-mirrors.mjs when building the public mirror.
const (
	SDKName    = "realuptime-errors-go"
	SDKVersion = "0.1.0"
)

const scrubbedPlaceholder = "[scrubbed]"

// WireFrame is one stack frame, post-scrub. ContextLine/PreContext/
// PostContext/Vars mirror packages/errors-js/types.ts's Phase 3 / v2
// additions; this SDK does not itself capture source context or local
// variables (Go strips them from a compiled binary and there is no
// portable stdlib-only way to read them), but the scrub function still
// handles them so a frame built by hand, or a value round-tripped through
// this type, scrubs identically to every other SDK -- the parity
// scrub-vectors.json pins.
type WireFrame struct {
	File        string            `json:"file"`
	Function    *string           `json:"function"`
	Line        *int              `json:"line"`
	InApp       bool              `json:"inApp"`
	Column      *int              `json:"column,omitempty"`
	ContextLine *string           `json:"contextLine,omitempty"`
	PreContext  []string          `json:"preContext,omitempty"`
	PostContext []string          `json:"postContext,omitempty"`
	Vars        map[string]string `json:"vars,omitempty"`
}

// WireBreadcrumb is one scrubbed trail entry, newest last on the wire.
type WireBreadcrumb struct {
	Timestamp *string           `json:"timestamp"`
	Category  *string           `json:"category"`
	Message   string            `json:"message"`
	Data      map[string]string `json:"data"`
}

// WireUser is who this happened to (v2, REA-182). ID rides as-is; Email and
// Username are "[scrubbed]" unless opted back in by exact dotted name via
// Config.AllowFields.
type WireUser struct {
	ID       string `json:"id,omitempty"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
}

// WireDevice is runtime/platform metadata read out of this process. Never
// hostname, local IP or a container id.
type WireDevice struct {
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	Platform       string `json:"platform,omitempty"`
	Arch           string `json:"arch,omitempty"`
}

// WireRequest is minimal request context: method, path, the matched route
// template, status. Never headers beyond what the caller deliberately
// attached, never cookies, never bodies.
type WireRequest struct {
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Status  *int              `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Route   string            `json:"route,omitempty"`
}

// WireEvent is one event on the wire, v1 fields first, then the strictly
// additive v2 (REA-182) fields.
type WireEvent struct {
	OccurredAt         string            `json:"occurredAt"`
	Message            string            `json:"message"`
	ExceptionType      *string           `json:"exceptionType"`
	Release            *string           `json:"release"`
	Environment        *string           `json:"environment"`
	Frames             []WireFrame       `json:"frames"`
	Request            *WireRequest      `json:"request"`
	Fingerprint        []string          `json:"fingerprint"`
	Breadcrumbs        []WireBreadcrumb  `json:"breadcrumbs"`
	BreadcrumbsDropped int               `json:"breadcrumbsDropped"`
	User               *WireUser         `json:"user,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	Context            map[string]string `json:"context,omitempty"`
	Device             *WireDevice       `json:"device,omitempty"`
}

// WireBatch is the ingest POST body.
type WireBatch struct {
	SDK           string      `json:"sdk"`
	DroppedClient int         `json:"droppedClient"`
	Events        []WireEvent `json:"events"`
}

// wireIngestResult is the success response body.
type wireIngestResult struct {
	Accepted         int  `json:"accepted"`
	DroppedOverQuota int  `json:"droppedOverQuota"`
	OverQuota        bool `json:"overQuota"`
	Quota            struct {
		Used     int    `json:"used"`
		Limit    int    `json:"limit"`
		ResetsAt string `json:"resetsAt"`
	} `json:"quota"`
}

// wireErrorBody is the shape of a refusal response.
type wireErrorBody struct {
	Reason   string `json:"reason"`
	Error    string `json:"error"`
	ResetsAt string `json:"resetsAt"`
}
