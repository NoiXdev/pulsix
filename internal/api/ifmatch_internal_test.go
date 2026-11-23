package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

func statusOf(err error) int {
	var se huma.StatusError
	if errors.As(err, &se) {
		return se.GetStatus()
	}
	return 0
}

func TestIfMatch(t *testing.T) {
	if got := revisionETag(7); got != `"7"` {
		t.Fatalf("revisionETag = %s", got)
	}
	cases := []struct {
		header string
		rev    int64
		status int // 0 = ok
	}{
		{`"7"`, 7, 0},
		{`W/"7"`, 7, 0},
		{` "12" `, 12, 0},
		{`7`, 0, http.StatusPreconditionFailed},
		{`"abc"`, 0, http.StatusPreconditionFailed},
		{`"0"`, 0, http.StatusPreconditionFailed},
		{`"`, 0, http.StatusPreconditionFailed},
		{`*`, 0, http.StatusPreconditionFailed},
		{``, 0, http.StatusPreconditionRequired},
	}
	for _, c := range cases {
		rev, err := requireIfMatch(c.header)
		if statusOf(err) != c.status || (c.status == 0 && rev != c.rev) {
			t.Errorf("requireIfMatch(%q) = %d, %v", c.header, rev, err)
		}
	}
	if rev, err := optionalIfMatch(""); rev != nil || err != nil {
		t.Fatalf("optionalIfMatch(\"\") = %v, %v", rev, err)
	}
	if rev, err := optionalIfMatch(`"3"`); err != nil || *rev != 3 {
		t.Fatalf("optionalIfMatch = %v, %v", rev, err)
	}
	// "*" is any current representation (RFC 9110): no revision to compare.
	if rev, err := optionalIfMatch(" * "); rev != nil || err != nil {
		t.Fatalf("optionalIfMatch(*) = %v, %v; want no revision and no error", rev, err)
	}
	if _, err := requireIfMatch("*"); statusOf(err) != http.StatusPreconditionFailed || !strings.Contains(err.Error(), "concrete revision") {
		t.Fatalf("requireIfMatch(*) = %v, want 412 naming the concrete revision", err)
	}
	if _, err := optionalIfMatch("junk"); statusOf(err) != http.StatusPreconditionFailed {
		t.Fatalf("optionalIfMatch(junk) = %v", err)
	}
}
