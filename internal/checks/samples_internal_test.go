package checks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCleanErrDropsNUL(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"inner NUL", "dis\x00k full", "disk full"},
		{"only NULs past the bound", strings.Repeat("\x00", MaxErrLen+10) + "x", "x"},
		{"plain text over the bound is cut", strings.Repeat("a", MaxErrLen+5), strings.Repeat("a", MaxErrLen)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanErr(tc.in); got != tc.want {
				t.Fatalf("cleanErr = %.40q (len %d), want %.40q (len %d)", got, len(got), tc.want, len(tc.want))
			}
		})
	}
}

func TestWithinContractRefusesNULInLabel(t *testing.T) {
	obs := func(label string) Observation {
		return Observation{
			HostID: uuid.New(), CheckKey: "service", Item: "nginx", Metric: "state",
			At: time.Now(), Label: &label,
		}
	}
	if err := withinContract(obs("act\x00ive")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("label with NUL: err = %v, want ErrInvalidInput", err)
	}
	if err := withinContract(obs("active")); err != nil {
		t.Fatalf("clean label: err = %v, want nil", err)
	}
}
