package cronexpr_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

func TestSimpleCompileAndDescribe(t *testing.T) {
	cases := []struct {
		in       cronexpr.Simple
		cron     string
		describe string
	}{
		{cronexpr.Simple{Kind: "every_minutes", Every: 1}, "* * * * *", "every minute"},
		{cronexpr.Simple{Kind: "every_minutes", Every: 30}, "*/30 * * * *", "every 30 min"},
		{cronexpr.Simple{Kind: "every_hours", Every: 1}, "0 * * * *", "every hour"},
		{cronexpr.Simple{Kind: "every_hours", Every: 6}, "0 */6 * * *", "every 6 h"},
		{cronexpr.Simple{Kind: "daily", Time: "03:00"}, "0 3 * * *", "daily at 03:00"},
		{cronexpr.Simple{Kind: "weekly", Days: []string{"thu", "tue"}, Time: "03:00"}, "0 3 * * 2,4", "Tue, Thu at 03:00"},
		{cronexpr.Simple{Kind: "weekly", Days: []string{"sun"}, Time: "23:45"}, "45 23 * * 0", "Sun at 23:45"},
		{cronexpr.Simple{Kind: "monthly", Day: 1, Time: "04:00"}, "0 4 1 * *", "monthly on day 1 at 04:00"},
	}
	for _, c := range cases {
		got, err := c.in.Compile()
		if err != nil || got != c.cron {
			t.Errorf("Compile(%+v) = %q, %v; want %q", c.in, got, err, c.cron)
		}
		if d := c.in.Describe(); d != c.describe {
			t.Errorf("Describe(%+v) = %q, want %q", c.in, d, c.describe)
		}
		// The compiled form round-trips to the same description.
		if d := cronexpr.Describe(got); d != c.describe {
			t.Errorf("Describe(%q) = %q, want %q", got, d, c.describe)
		}
	}
}

func TestSimpleCompileRejects(t *testing.T) {
	for _, in := range []cronexpr.Simple{
		{Kind: "every_minutes", Every: 7},
		{Kind: "every_minutes", Every: 0},
		{Kind: "every_hours", Every: 5},
		{Kind: "daily", Time: "24:00"},
		{Kind: "daily", Time: "3:00"},
		{Kind: "daily"},
		{Kind: "weekly", Time: "03:00"},
		{Kind: "weekly", Days: []string{"funday"}, Time: "03:00"},
		{Kind: "weekly", Days: []string{"mon", "mon"}, Time: "03:00"},
		{Kind: "monthly", Day: 0, Time: "03:00"},
		{Kind: "monthly", Day: 32, Time: "03:00"},
		{Kind: "yearly"},
	} {
		if _, err := in.Compile(); err == nil {
			t.Errorf("Compile(%+v) accepted", in)
		}
	}
}

func TestDescribeUnknownShape(t *testing.T) {
	for _, spec := range []string{"0 9-17 * * 1-5", "5 4 * 1 *", "garbage"} {
		if d := cronexpr.Describe(spec); d != "" {
			t.Errorf("Describe(%q) = %q, want empty", spec, d)
		}
	}
	if d := cronexpr.Describe("@daily"); d != "daily at 00:00" {
		t.Errorf("Describe(@daily) = %q", d)
	}
}
