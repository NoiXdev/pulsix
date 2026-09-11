package api

import (
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// revisionETag renders an object's revision as a strong ETag: "7".
func revisionETag(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// requireIfMatch reads the revision a write is based on. Missing is 428,
// anything that is not an ETag this API issued is 412 (spec §15).
func requireIfMatch(header string) (int64, error) {
	rev, ok, err := parseIfMatch(header)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, huma.Error428PreconditionRequired("send If-Match with the ETag of the version you edited")
	}
	return rev, nil
}

// optionalIfMatch reads If-Match when the client sent one.
func optionalIfMatch(header string) (*int64, error) {
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
