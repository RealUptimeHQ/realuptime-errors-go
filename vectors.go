package realuptimeerrors

import _ "embed"

// vendoredScrubVectors is errors-js/scrub-vectors.json, vendored
// into this module (this SDK ships as a standalone public mirror, so a
// parity claim depending on a sibling directory in the monorepo would be
// untestable there). vendored_vectors_test.go pins it byte-for-byte against
// the canonical file, and scripts/publish-sdk-mirrors.mjs re-copies the
// canonical file at export time so this vendored copy can never silently
// drift in the published mirror.
//
//go:embed scrub-vectors.json
var vendoredScrubVectors []byte
