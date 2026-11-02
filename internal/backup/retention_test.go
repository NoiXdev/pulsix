package backup_test

import (
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// objAt builds a listed object whose header carries at and kind.
func objAt(at time.Time, kind string) backup.Object {
	return backup.Object{
		Name:   backup.ObjectName(at, kind),
		Header: backup.Header{FormatVersion: backup.FormatVersion, CreatedAt: at, Kind: kind},
	}
}

func TestPruneKeepsDailyWeeklyMonthly(t *testing.T) {
	loc := berlin(t)
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, loc)

	var objs []backup.Object
	for i := range 120 {
		objs = append(objs, objAt(end.AddDate(0, 0, -i), "scheduled"))
	}
	// Three manual backups sprinkled between, far outside any bucket.
	manual := []backup.Object{
		objAt(end.AddDate(0, 0, -50).Add(time.Hour), "manual"),
		objAt(end.AddDate(0, 0, -90).Add(time.Hour), "manual"),
		objAt(end.AddDate(0, 0, -110).Add(time.Hour), "manual"),
	}
	objs = append(objs, manual...)

	remove := backup.Prune(objs, backup.Retention{Daily: 7, Weekly: 4, Monthly: 6}, loc)

	// Newest 7 days: Nov 2 .. Oct 27. Newest of the 4 newest ISO weeks
	// (Nov 2; Nov 1; Oct 25; Oct 18). Newest of the 6 newest months
	// (Nov 2; Oct 31; Sep 30; Aug 31; Jul 31; June does not exist, since 120
	// days back from Nov 2 is Jul 5, so only five months are kept).
	kept := []time.Time{
		time.Date(2026, 11, 2, 3, 0, 0, 0, loc),
		time.Date(2026, 11, 1, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 31, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 30, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 29, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 28, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 27, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 25, 3, 0, 0, 0, loc),
		time.Date(2026, 10, 18, 3, 0, 0, 0, loc),
		time.Date(2026, 9, 30, 3, 0, 0, 0, loc),
		time.Date(2026, 8, 31, 3, 0, 0, 0, loc),
		time.Date(2026, 7, 31, 3, 0, 0, 0, loc),
	}
	wantKept := map[string]bool{}
	for _, at := range kept {
		wantKept[backup.ObjectName(at, "scheduled")] = true
	}

	removed := map[string]bool{}
	for _, n := range remove {
		if removed[n] {
			t.Errorf("%s listed twice", n)
		}
		removed[n] = true
	}
	for _, m := range manual {
		if removed[m.Name] {
			t.Errorf("manual object %s must never be removed", m.Name)
		}
	}
	for _, o := range objs {
		if o.Header.Kind != "scheduled" {
			continue
		}
		if wantKept[o.Name] == removed[o.Name] {
			t.Errorf("%s: kept=%v removed=%v, want kept=%v", o.Name, !removed[o.Name], removed[o.Name], wantKept[o.Name])
		}
	}
	if got, want := len(remove), 120-len(kept); got != want {
		t.Errorf("removed %d objects, want %d", got, want)
	}
}

func TestPruneZeroRetentionStillKeepsNewest(t *testing.T) {
	loc := berlin(t)
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, loc)
	var objs []backup.Object
	for i := range 5 {
		objs = append(objs, objAt(end.AddDate(0, 0, -i), "scheduled"))
	}
	objs = append(objs, objAt(end.Add(time.Hour), "manual"))

	remove := backup.Prune(objs, backup.Retention{}, loc)

	newest := backup.ObjectName(end, "scheduled")
	if len(remove) != 4 || slices.Contains(remove, newest) {
		t.Fatalf("remove = %v, want the 4 older scheduled objects", remove)
	}
}

func TestPruneUsesNameWhenHeaderIsBroken(t *testing.T) {
	loc := berlin(t)
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, loc)
	var objs []backup.Object
	for i := range 3 {
		o := backup.Object{
			Name:      backup.ObjectName(end.AddDate(0, 0, -i), "scheduled"),
			HeaderErr: backup.ErrNotABackup,
		}
		objs = append(objs, o)
	}
	// No parseable name and no header: never pruned.
	objs = append(objs, backup.Object{Name: "mystery.pxbak", HeaderErr: backup.ErrNotABackup})

	remove := backup.Prune(objs, backup.Retention{Daily: 1}, loc)

	if len(remove) != 2 || slices.Contains(remove, "mystery.pxbak") {
		t.Fatalf("remove = %v, want the two oldest named objects", remove)
	}
}

func TestPruneBucketsByLocalDay(t *testing.T) {
	loc := berlin(t)
	// 23:30Z on Nov 1 is 00:30 on Nov 2 in Berlin: same local day as the
	// 12:00Z object, so with Daily=1 only the newer one stays.
	a := objAt(time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC), "scheduled")
	b := objAt(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), "scheduled")
	c := objAt(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), "scheduled")

	remove := backup.Prune([]backup.Object{a, b, c}, backup.Retention{Daily: 1}, loc)

	if len(remove) != 2 || !slices.Contains(remove, a.Name) || !slices.Contains(remove, c.Name) {
		t.Fatalf("remove = %v", remove)
	}
	if got := backup.Prune([]backup.Object{a, b}, backup.Retention{Daily: 2}, nil); len(got) != 0 {
		t.Fatalf("UTC buckets: remove = %v, want none", got)
	}
}

func TestPruneEmpty(t *testing.T) {
	if got := backup.Prune(nil, backup.Retention{Daily: 7}, time.UTC); len(got) != 0 {
		t.Fatalf("remove = %v", got)
	}
}
