package probe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTargetHost(t *testing.T) {
	tests := []struct {
		in, want string
		err      error
	}{
		{"example.com", "example.com", nil},
		{" example.com ", "example.com", nil},
		{"192.0.2.1", "192.0.2.1", nil},
		{"2001:db8::1", "2001:db8::1", nil},
		{"[2001:db8::1]", "2001:db8::1", nil},
		{"fe80::1%eth0", "fe80::1%eth0", nil},
		{"", "", errNoAddress},
		{"   ", "", errNoAddress},
		{"host:22", "", errBadTarget},
		{"[example.com]", "", errBadTarget},
		{"http://example.com", "", errBadTarget},
		{"exa mple.com", "", errBadTarget},
		{"example.com/path", "", errBadTarget},
		{"under_score-host.example", "under_score-host.example", nil},
		{strings.Repeat("a", 253), strings.Repeat("a", 253), nil},
		{strings.Repeat("a", 254), "", errBadTarget},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := TargetHost(tc.in)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("TargetHost(%q) = %q, %v; want %q, %v", tc.in, got, err, tc.want, tc.err)
			}
		})
	}
}

func TestBoundUsesTheEarlierDeadline(t *testing.T) {
	ctx, cancel := Bound(context.Background(), map[string]any{"timeout_s": float64(2)}, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 2*time.Second || time.Until(dl) < time.Second {
		t.Fatalf("params timeout not applied: %v", time.Until(dl))
	}

	ctx, cancel = Bound(context.Background(), nil, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 5*time.Second || time.Until(dl) < 4*time.Second {
		t.Fatalf("default timeout not applied: %v", time.Until(dl))
	}

	parent, cancelParent := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelParent()
	ctx, cancel = Bound(parent, map[string]any{"timeout_s": float64(30)}, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 200*time.Millisecond {
		t.Fatalf("parent deadline not kept: %v", time.Until(dl))
	}
}
