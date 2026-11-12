package checkplugins_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// paramCase is one row of a ported accept/refuse table.
type paramCase struct {
	name   string
	plugin string
	params map[string]any
	ok     bool
}

func runParamCases(t *testing.T, cases []paramCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkplugins.Validate(tc.plugin, tc.params)
			if tc.ok && err != nil {
				t.Fatalf("Validate(%s, %v) = %v, want nil", tc.plugin, tc.params, err)
			}
			if !tc.ok && !errors.Is(err, checkplugins.ErrInvalidParams) {
				t.Fatalf("Validate(%s, %v) = %v, want ErrInvalidParams", tc.plugin, tc.params, err)
			}
		})
	}
}

func anyList(n int, entry string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = entry
	}
	return out
}

// dnsName is a valid DNS name of exactly n bytes: 49-byte labels.
func dnsName(n int) string {
	var b strings.Builder
	for b.Len() < n {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strings.Repeat("a", min(49, n-b.Len())))
	}
	return b.String()
}
