package agents_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
)

// TestRecordConfigCheckBoundsWhatTheAgentSent: the reported version is the
// agent's to fill, so the manager stores at most MaxConfigVersionBytes of
// it as one line of valid UTF-8 — a NUL (which Postgres text refuses) or a
// megabyte of junk must neither fail the write nor be stored whole. A real
// version (hex SHA-256) passes unchanged.
func TestRecordConfigCheckBoundsWhatTheAgentSent(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name, reported, want string
	}{
		{"a real version", sha, sha},
		{"empty", "", ""},
		{"control characters", "ab\x00c\nd", "ab c d"},
		{"invalid UTF-8", "ab\xffc", "ab�c"},
		{"over the cap", strings.Repeat("ä", agents.MaxConfigVersionBytes), strings.Repeat("ä", agents.MaxConfigVersionBytes/2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, _, _ := newService(t)
			org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-c")); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordConfigCheck(ctx, "serial-c", c.reported, sha); err != nil {
				t.Fatal(err)
			}
			got, err := svc.GetByHost(ctx, org, host)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Config.Known || got.Config.Reported != c.want || got.Config.Expected != sha {
				t.Fatalf("config = %+v; want reported %q against %q", got.Config, c.want, sha)
			}
			if len(got.Config.Reported) > agents.MaxConfigVersionBytes || !utf8.ValidString(got.Config.Reported) {
				t.Fatalf("stored %d bytes (valid UTF-8: %v)", len(got.Config.Reported), utf8.ValidString(got.Config.Reported))
			}
		})
	}
}

// TestConfigCheckCurrent: current means a known check whose two versions
// are equal; an unknown check is never current.
func TestConfigCheckCurrent(t *testing.T) {
	cases := []struct {
		c    agents.ConfigCheck
		want bool
	}{
		{agents.ConfigCheck{}, false},
		{agents.ConfigCheck{Known: true, Reported: "a", Expected: "a"}, true},
		{agents.ConfigCheck{Known: true, Reported: "a", Expected: "b"}, false},
		{agents.ConfigCheck{Known: true, Reported: "", Expected: "b"}, false},
	}
	for _, c := range cases {
		if got := c.c.Current(); got != c.want {
			t.Errorf("%+v.Current() = %v, want %v", c.c, got, c.want)
		}
	}
}
