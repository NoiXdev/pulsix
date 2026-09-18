package cronexpr_test

import (
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

func TestParseAccepts(t *testing.T) {
	cases := map[string]string{
		"0 3 * * 2,4":          "0 3 * * 2,4",
		" 0   3 * *  2,4 ":     "0 3 * * 2,4",
		"*/15 * * * *":         "*/15 * * * *",
		"0 9-17/2 * * MON-FRI": "0 9-17/2 * * MON-FRI",
		"0 0 1 jan,Jul *":      "0 0 1 jan,Jul *",
		"0 0 * * 7":            "0 0 * * 7",
		"0 0 ? * sun":          "0 0 ? * sun",
		"5/10 * * * *":         "5/10 * * * *",
		"@hourly":              "@hourly",
		"@DAILY":               "@daily",
		"@weekly":              "@weekly",
		"@monthly":             "@monthly",
	}
	for in, want := range cases {
		e, err := cronexpr.Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if e.String() != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, e.String(), want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * 32 * *", "* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *",
		"*/61 * * * *", "1,,2 * * * *", "a * * * *", "* * * foo *", "@yearly",
		"@every 5m", "@reboot", "CRON_TZ=UTC 0 * * * *", "0 3 L * *", "0 3 * * 5#2",
	} {
		if _, err := cronexpr.Parse(in); !errors.Is(err, cronexpr.ErrSyntax) {
			t.Errorf("Parse(%q) = %v, want ErrSyntax", in, err)
		}
	}
}
