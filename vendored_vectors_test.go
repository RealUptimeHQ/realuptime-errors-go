package realuptimeerrors

import (
	"os"
	"path/filepath"
	"testing"
)

// Pins the vendored copy of scrub-vectors.json byte-for-byte against
// errors-js/scrub-vectors.json, the canonical file. This is what
// makes the mirror export in scripts/publish-sdk-mirrors.mjs safe to
// re-copy blindly rather than trusting the module's own copy has not
// drifted (the internal-docs repo's errors-plan.md, "The SDK matrix").
func TestVendoredVectorsMatchCanonical(t *testing.T) {
	canonicalPath := filepath.Join("..", "errors-js", "scrub-vectors.json")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Skipf("cannot read %s from this checkout (expected when this module is exported standalone as the public mirror): %v", canonicalPath, err)
	}
	if string(canonical) != string(vendoredScrubVectors) {
		t.Fatalf("errors-go/scrub-vectors.json has drifted from %s; re-copy it byte for byte", canonicalPath)
	}
}
