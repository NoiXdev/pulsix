package api

import (
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// revisionETag renders an object's revision as a strong ETag: "7".
func revisionETag(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// If-Match on revisioned routes has two behaviours, one helper each; a
// route picks the one that fits it (grep the helpers for the current
// lists). A route that requires the header (requireIfMatch) forces a read
// before the write, so it needs the concrete revision ETag and refuses "*"
// with 412 saying so. A route where the header is optional
// (optionalIfMatch) takes "*" as "any current revision" (RFC 9110) and
// saves without a comparison, as it does without the header.

// requireIfMatch reads the revision a write is based on. Missing is 428,
// "*" and anything else that is not an ETag this API issued is 412
// (spec §15).
func requireIfMatch(header string) (int64, error) {
	if strings.TrimSpace(header) == "*" {
		return 0, huma.Error412PreconditionFailed("this route needs the concrete revision ETag of the version you edited; If-Match: * is not accepted here")
	}
	rev, ok, err := parseIfMatch(header)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, huma.Error428PreconditionRequired("send If-Match with the ETag of the version you edited")
	}
	return rev, nil
}

// optionalIfMatch reads If-Match when the client sent one. "*" matches
// whatever revision is current (RFC 9110), so it names none: nil, like no
// header. The required routes still refuse it, as they need the revision
// the write is based on.
func optionalIfMatch(header string) (*int64, error) {
	if strings.TrimSpace(header) == "*" {
		return nil, nil
	}
	rev, ok, err := parseIfMatch(header)
	if err != nil || !ok {
		return nil, err
	}
	return &rev, nil
}

func parseIfMatch(header string) (int64, bool, error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, false, nil
	}
	v := strings.TrimPrefix(h, "W/")
	bad := huma.Error412PreconditionFailed("If-Match must be the quoted ETag of the version you edited")
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return 0, false, bad
	}
	n, err := strconv.ParseInt(v[1:len(v)-1], 10, 64)
	if err != nil || n < 1 {
		return 0, false, bad
	}
	return n, true, nil
}
