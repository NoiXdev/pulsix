package backup_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

func TestObjectNameRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	got := backup.ObjectName(at, "scheduled")
	if want := "pulsix-20261102T030000Z-scheduled.pxbak"; got != want {
		t.Fatalf("ObjectName = %q, want %q", got, want)
	}

	// A non-UTC time is written in UTC.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	if got := backup.ObjectName(at.In(berlin), "manual"); got != "pulsix-20261102T030000Z-manual.pxbak" {
		t.Fatalf("ObjectName(berlin) = %q", got)
	}

	parsedAt, kind, ok := backup.ParseObjectName(got)
	if !ok || kind != "scheduled" || !parsedAt.Equal(at) {
		t.Fatalf("ParseObjectName(%q) = %v, %q, %v", got, parsedAt, kind, ok)
	}
}

func TestParseObjectNameRejects(t *testing.T) {
	for _, name := range []string{
		"foo.txt",
		"",
		"pulsix-20261102T030000Z-scheduled.pxbak.partial",
		"pulsix-20261102T030000Z-other.pxbak",
		"pulsix-20261102T030000-scheduled.pxbak",
		"pulsix-20261302T030000Z-scheduled.pxbak",
		"pulsix-probe-ab12.tmp",
		"x/pulsix-20261102T030000Z-scheduled.pxbak",
		// Aliases Go's time parser accepts for 03:00:00: a fraction after
		// the seconds, with a dot or a comma. ObjectName never writes one.
		"pulsix-20261102T030000.0Z-scheduled.pxbak",
		"pulsix-20261102T030000.000000000Z-scheduled.pxbak",
		"pulsix-20261102T030000,0Z-scheduled.pxbak",
	} {
		if _, _, ok := backup.ParseObjectName(name); ok {
			t.Errorf("ParseObjectName(%q) accepted", name)
		}
	}
}
