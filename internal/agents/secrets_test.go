package agents_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
)

// heartbeatSecrets sends one heartbeat through the service and returns the
// report the store received.
func heartbeatSecrets(t *testing.T, names []string, state string) agents.SecretsReport {
	t.Helper()
	svc, _, _, _ := newService(t)
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-s")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "serial-s", "v2", names, state); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetByHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v2" || got.LastSeen == nil {
		t.Fatalf("heartbeat did not record last_seen and version: %+v", got)
	}
	return got.Secrets
}

func TestHeartbeatRecordsSecrets(t *testing.T) {
	got := heartbeatSecrets(t, []string{"mysql-prod", "a.b_c-1"}, "ok")
	if !got.Known || got.State != "ok" || !slices.Equal(got.Names, []string{"a.b_c-1", "mysql-prod"}) {
		t.Fatalf("report = %+v, want known, ok, the names sorted", got)
	}
}

// An agent that reports zero secrets is known to have none: the names are
// an empty, non-nil list, never the "unknown" nil.
func TestHeartbeatZeroSecretsIsKnown(t *testing.T) {
	got := heartbeatSecrets(t, nil, "absent")
	if !got.Known || got.State != "absent" || got.Names == nil || len(got.Names) != 0 {
		t.Fatalf("report = %+v, want known, absent, an empty non-nil list", got)
	}
}

// An agent older than agent.yaml sends neither field; proto3 cannot tell
// an absent repeated field from an empty one, so the state is the tell:
// empty state means unknown, whatever names ride along.
func TestHeartbeatOldAgentIsUnknown(t *testing.T) {
	for _, names := range [][]string{nil, {"stray"}} {
		got := heartbeatSecrets(t, names, "")
		if got.Known || got.Names != nil || got.State != "" {
			t.Fatalf("names %v with no state: report = %+v, want unknown", names, got)
		}
	}
}

func TestHeartbeatDropsJunkNames(t *testing.T) {
	names := []string{"ok-1", "", "a b", "ä", "a/b", "x\x00", strings.Repeat("n", 65), strings.Repeat("n", 64), "ok-1", "{x}"}
	got := heartbeatSecrets(t, names, "ok")
	want := []string{strings.Repeat("n", 64), "ok-1"}
	if !got.Known || !slices.Equal(got.Names, want) {
		t.Fatalf("names = %q, want %q (invalid dropped, duplicates once, sorted)", got.Names, want)
	}
}

func TestHeartbeatCapsNames(t *testing.T) {
	var names []string
	for i := range 300 {
		names = append(names, fmt.Sprintf("s%03d", 299-i))
	}
	got := heartbeatSecrets(t, names, "ok")
	if len(got.Names) != agents.MaxSecretNames || agents.MaxSecretNames != 256 {
		t.Fatalf("kept %d names, want %d", len(got.Names), 256)
	}
	if got.Names[0] != "s000" || got.Names[255] != "s255" || !slices.IsSorted(got.Names) {
		t.Fatalf("kept %q…%q, want the first 256 in sorted order", got.Names[0], got.Names[255])
	}
}

func TestHeartbeatBoundsTheState(t *testing.T) {
	cases := []struct {
		name, in string
		check    func(t *testing.T, got string)
	}{
		{"cut on a rune boundary", strings.Repeat("€", 1000), func(t *testing.T, got string) {
			if got != strings.Repeat("€", 682) {
				t.Fatalf("len %d, want 682 whole runes (2046 bytes)", len(got))
			}
		}},
		{"exactly the cap", strings.Repeat("e", 2048), func(t *testing.T, got string) {
			if len(got) != 2048 {
				t.Fatalf("len %d, want 2048", len(got))
			}
		}},
		{"invalid UTF-8", "bad \xff\xfe state", func(t *testing.T, got string) {
			if got != "bad � state" {
				t.Fatalf("got %q, want the invalid bytes replaced", got)
			}
		}},
		{"control characters", "line 4\x00:\nmapping\tvalues", func(t *testing.T, got string) {
			if got != "line 4 : mapping values" {
				t.Fatalf("got %q, want control characters as spaces", got)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := heartbeatSecrets(t, nil, tc.in)
			if !got.Known || len(got.State) > agents.MaxSecretsStateBytes || !utf8.ValidString(got.State) {
				t.Fatalf("report = %+v, want known, valid UTF-8, at most %d bytes", got, agents.MaxSecretsStateBytes)
			}
			tc.check(t, got.State)
		})
	}
}
