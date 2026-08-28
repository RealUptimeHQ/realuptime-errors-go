package realuptimeerrors

import (
	"regexp"
	"strings"
)

// PII scrub-by-default, client-side (the internal-docs repo's errors-plan.md, "PII
// scrub-by-default, specified"). A port of packages/errors-js/scrub.ts;
// the shared vectors (scrub-vectors.json, vendored into this module and
// asserted byte-identical to packages/errors-js/scrub-vectors.json by
// wire_contract_test.go) pin every implementation -- JS, Python, Ruby, this
// one, and the server's second net -- to identical output.
//
// The five pattern rules, in the shared order:
//  1. JWT three-dot shape
//  2. prefixed keys (sk_/pk_/rk_/ghp_/gho_/ghs_/xox?_/rua_/rue_/ru_live_)
//  3. card-shaped digit runs, 13-19 digits, Luhn-checked
//  4. long hex runs (32+)
//  5. long base64-ish runs (40+)
//
// RE2 (Go's regexp) has no lookaround, unlike the JS/Ruby/Python
// implementations' regexes for rules 3 and 5. scrubCardNumbers and
// scrubBase64ish below are hand-written scanners chosen to produce the same
// output as the lookaround-based regexes for every vector in
// scrub-vectors.json (pinned by vendored_vectors_test.go), rather than an
// approximation.
const scrubbed = scrubbedPlaceholder

var (
	jwtRE         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b`)
	prefixedKeyRE = regexp.MustCompile(`\b(?:sk|pk|rk|ghp|gho|ghs|xox[a-z]|rua|rue|ru_live|whsec)_[A-Za-z0-9_-]{8,}`)
	awsKeyRE      = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	hexRE         = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	urlCredsRE    = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*)://[^\s/@]+:[^\s/@]+@`)
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	keySeparator  = regexp.MustCompile(`[^a-zA-Z0-9]+`)
)

// keyDenylist (REA-474): see packages/errors-js/scrub.ts's module doc for
// the full rationale. Matched against whole camelCase/snake_case segments
// (and contiguous joins of them) so "tokenizer" and "authentic" never match
// "token" / "auth".
var keyDenylist = map[string]bool{
	"password":      true,
	"passwd":        true,
	"secret":        true,
	"token":         true,
	"apikey":        true,
	"auth":          true,
	"authorization": true,
	"credential":    true,
	"credentials":   true,
	"privatekey":    true,
	"accesskey":     true,
	"session":       true,
}

func keySegments(key string) []string {
	spaced := camelBoundary.ReplaceAllString(key, "${1}_${2}")
	parts := keySeparator.Split(spaced, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

// isKeyDenylisted is true when some contiguous run of a key's segments,
// joined with no separator, equals a denylisted word exactly.
func isKeyDenylisted(key string) bool {
	segments := keySegments(key)
	for i := range segments {
		joined := ""
		for j := i; j < len(segments); j++ {
			joined += segments[j]
			if keyDenylist[joined] {
				return true
			}
		}
	}
	return false
}

var removedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
}

// removedUserFields are the exact dotted names Config.AllowFields can opt
// back in. user.id is deliberately absent: see the package doc in wire.go.
var removedUserFields = []string{"user.email", "user.username"}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func luhnValid(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// scrubCardNumbers replicates /(?<!\d)(?:\d[ -]?){12,18}\d(?!\d)/g: a
// maximal run of digits with optional single space/dash separators between
// them, not itself adjacent to another digit, replaced whole when its
// digit count is 13-19 and it passes Luhn.
func scrubCardNumbers(s string) string {
	var b strings.Builder
	n := len(s)
	i := 0
	for i < n {
		if isDigit(s[i]) && (i == 0 || !isDigit(s[i-1])) {
			j := i
			digitCount := 0
			for j < n {
				if isDigit(s[j]) {
					digitCount++
					j++
					continue
				}
				if (s[j] == ' ' || s[j] == '-') && j+1 < n && isDigit(s[j+1]) {
					j++
					continue
				}
				break
			}
			run := s[i:j]
			if digitCount >= 13 && digitCount <= 19 {
				digitsOnly := strings.Map(func(r rune) rune {
					if r == ' ' || r == '-' {
						return -1
					}
					return r
				}, run)
				if luhnValid(digitsOnly) {
					b.WriteString(scrubbed)
					i = j
					continue
				}
			}
			b.WriteString(run)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isBase64Char(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '+' || b == '_' || b == '-'
}

func isBase64FullSet(b byte) bool { return isBase64Char(b) || b == '=' }

// scrubBase64ish replicates
// /(?<![A-Za-z0-9+_=-])[A-Za-z0-9+_-]{40,}={0,2}/g: a maximal run of
// base64url-ish characters (excluding '='), not itself immediately preceded
// by another character from the full set (including '='), followed by up to
// two trailing '=' padding characters, replaced whole when the run itself
// is 40+ characters.
func scrubBase64ish(s string) string {
	var b strings.Builder
	n := len(s)
	i := 0
	for i < n {
		if isBase64Char(s[i]) && (i == 0 || !isBase64FullSet(s[i-1])) {
			j := i
			for j < n && isBase64Char(s[j]) {
				j++
			}
			runLen := j - i
			end := j
			for end < n && s[end] == '=' && end-j < 2 {
				end++
			}
			if runLen >= 40 {
				b.WriteString(scrubbed)
				i = end
				continue
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// scrubString applies the five pattern rules to one string. Pure; exported
// so vector tests can drive it directly.
func scrubString(value string) string {
	out := urlCredsRE.ReplaceAllString(value, "${1}://"+scrubbed+"@")
	out = jwtRE.ReplaceAllString(out, scrubbed)
	out = prefixedKeyRE.ReplaceAllString(out, scrubbed)
	out = awsKeyRE.ReplaceAllString(out, scrubbed)
	out = scrubCardNumbers(out)
	out = hexRE.ReplaceAllString(out, scrubbed)
	out = scrubBase64ish(out)
	return out
}

// scrubStringMap scrubs VALUES of a flat string map (tags, custom context,
// device, frame locals); KEYS are left alone -- a key is a label the
// integrator chose.
func scrubStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if isKeyDenylisted(k) {
			out[k] = scrubbed
		} else {
			out[k] = scrubString(v)
		}
	}
	return out
}

func scrubBreadcrumb(c WireBreadcrumb) WireBreadcrumb {
	out := c
	out.Message = scrubString(c.Message)
	out.Data = scrubStringMap(c.Data)
	return out
}

func scrubUser(u WireUser, allowed map[string]bool) WireUser {
	out := u
	if out.ID != "" {
		out.ID = scrubString(out.ID)
	}
	for _, field := range removedUserFields {
		key := strings.TrimPrefix(field, "user.")
		var value string
		switch key {
		case "email":
			value = u.Email
		case "username":
			value = u.Username
		}
		if value == "" {
			continue
		}
		var scrubbedValue string
		if allowed[field] {
			scrubbedValue = scrubString(value)
		} else {
			scrubbedValue = scrubbed
		}
		switch key {
		case "email":
			out.Email = scrubbedValue
		case "username":
			out.Username = scrubbedValue
		}
	}
	return out
}

// scrubFrame scrubs a frame's source-context and locals, when present.
// File, function, line and column are never scrubbed: they are the
// product.
func scrubFrame(f WireFrame) WireFrame {
	if f.ContextLine == nil && len(f.PreContext) == 0 && len(f.PostContext) == 0 && f.Vars == nil {
		return f
	}
	out := f
	if f.ContextLine != nil {
		v := scrubString(*f.ContextLine)
		out.ContextLine = &v
	}
	if f.PreContext != nil {
		pre := make([]string, len(f.PreContext))
		for i, line := range f.PreContext {
			pre[i] = scrubString(line)
		}
		out.PreContext = pre
	}
	if f.PostContext != nil {
		post := make([]string, len(f.PostContext))
		for i, line := range f.PostContext {
			post[i] = scrubString(line)
		}
		out.PostContext = post
	}
	if f.Vars != nil {
		out.Vars = scrubStringMap(f.Vars)
	}
	return out
}

func scrubRequest(r WireRequest, allowed map[string]bool) WireRequest {
	out := r
	if out.Path != "" {
		out.Path = scrubString(out.Path)
	}
	if out.Route != "" {
		out.Route = scrubString(out.Route)
	}
	if out.Headers != nil {
		headers := make(map[string]string, len(out.Headers))
		for name, value := range out.Headers {
			lower := strings.ToLower(name)
			if (removedHeaders[lower] || isKeyDenylisted(lower)) && !allowed[lower] {
				headers[name] = scrubbed
			} else {
				headers[name] = scrubString(value)
			}
		}
		out.Headers = headers
	}
	return out
}

// scrubEvent scrubs one wire event. Returns a new value; never mutates the
// input in a way visible to the caller. v2 fields are touched only when
// present, so a v1-shaped event scrubs to a v1-shaped event.
func scrubEvent(event WireEvent, allowFields []string) WireEvent {
	allowed := make(map[string]bool, len(allowFields))
	for _, f := range allowFields {
		allowed[strings.ToLower(f)] = true
	}
	out := event
	out.Message = scrubString(event.Message)
	if event.Breadcrumbs != nil {
		crumbs := make([]WireBreadcrumb, len(event.Breadcrumbs))
		for i, c := range event.Breadcrumbs {
			crumbs[i] = scrubBreadcrumb(c)
		}
		out.Breadcrumbs = crumbs
	}
	if event.Request != nil {
		req := scrubRequest(*event.Request, allowed)
		out.Request = &req
	}
	if event.Frames != nil {
		frames := make([]WireFrame, len(event.Frames))
		for i, f := range event.Frames {
			frames[i] = scrubFrame(f)
		}
		out.Frames = frames
	}
	if event.User != nil {
		u := scrubUser(*event.User, allowed)
		out.User = &u
	}
	out.Tags = scrubStringMap(event.Tags)
	out.Context = scrubStringMap(event.Context)
	if event.Device != nil {
		d := *event.Device
		asMap := map[string]string{}
		if d.Runtime != "" {
			asMap["runtime"] = d.Runtime
		}
		if d.RuntimeVersion != "" {
			asMap["runtimeVersion"] = d.RuntimeVersion
		}
		if d.Platform != "" {
			asMap["platform"] = d.Platform
		}
		if d.Arch != "" {
			asMap["arch"] = d.Arch
		}
		scrubbedMap := scrubStringMap(asMap)
		d.Runtime = scrubbedMap["runtime"]
		d.RuntimeVersion = scrubbedMap["runtimeVersion"]
		d.Platform = scrubbedMap["platform"]
		d.Arch = scrubbedMap["arch"]
		out.Device = &d
	}
	return out
}
