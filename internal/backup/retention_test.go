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

// objAt builds a listed, verified object whose header carries at and kind.
func objAt(at time.Time, kind string) backup.Object {
	return backup.Object{
		Name:     backup.ObjectName(at, kind),
		Header:   backup.Header{FormatVersion: backup.FormatVersion, CreatedAt: at, Kind: kind},
		Verified: true,
	}
}

// unverifiedAt is objAt without the verification: a header this instance
// cannot authenticate.
func unverifiedAt(at time.Time, kind string) backup.Object {
	o := objAt(at, kind)
	o.Verified = false
	return o
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

	remove := backup.Prune(objs, backup.Retention{Daily: 7, Weekly: 4, Monthly: 6}, loc, "", end)

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

	remove := backup.Prune(objs, backup.Retention{}, loc, "", end)

	newest := backup.ObjectName(end, "scheduled")
	if len(remove) != 4 || slices.Contains(remove, newest) {
		t.Fatalf("remove = %v, want the 4 older scheduled objects", remove)
	}
}

func TestPruneLeavesDamagedObjectsAlone(t *testing.T) {
	loc := berlin(t)
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, loc)
	var objs []backup.Object
	for i := 1; i <= 3; i++ {
		objs = append(objs, objAt(end.AddDate(0, 0, -i), "scheduled"))
	}
	// The newest object by name has a broken header: it must neither be
	// removed nor count as the newest kept backup.
	broken := backup.Object{Name: backup.ObjectName(end, "scheduled"), HeaderErr: backup.ErrNotABackup}
	objs = append(objs, broken, backup.Object{Name: "mystery.pxbak", HeaderErr: backup.ErrNotABackup})

	remove := backup.Prune(objs, backup.Retention{Daily: 1}, loc, "", end)

	if slices.Contains(remove, broken.Name) || slices.Contains(remove, "mystery.pxbak") {
		t.Fatalf("remove = %v, must not name a damaged object", remove)
	}
	if slices.Contains(remove, objs[0].Name) {
		t.Fatalf("remove = %v, the newest valid backup %s must stay", remove, objs[0].Name)
	}
	if len(remove) != 2 {
		t.Fatalf("remove = %v, want the two older valid objects", remove)
	}
}

func TestPruneBucketsByLocalDay(t *testing.T) {
	loc := berlin(t)
	// 23:30Z on Nov 1 is 00:30 on Nov 2 in Berlin: same local day as the
	// 12:00Z object, so with Daily=1 only the newer one stays.
	a := objAt(time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC), "scheduled")
	b := objAt(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), "scheduled")
	c := objAt(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), "scheduled")

	remove := backup.Prune([]backup.Object{a, b, c}, backup.Retention{Daily: 1}, loc, "", b.Header.CreatedAt)

	if len(remove) != 2 || !slices.Contains(remove, a.Name) || !slices.Contains(remove, c.Name) {
		t.Fatalf("remove = %v", remove)
	}
	if got := backup.Prune([]backup.Object{a, b}, backup.Retention{Daily: 2}, nil, "", b.Header.CreatedAt); len(got) != 0 {
		t.Fatalf("UTC buckets: remove = %v, want none", got)
	}
}

func TestPruneEmpty(t *testing.T) {
	if got := backup.Prune(nil, backup.Retention{Daily: 7}, time.UTC, "", time.Now()); len(got) != 0 {
		t.Fatalf("remove = %v", got)
	}
}

func TestPruneNeverRemovesKeep(t *testing.T) {
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	oldest := objAt(end.AddDate(0, 0, -4), backup.KindScheduled)
	objs := []backup.Object{oldest}
	for i := range 4 {
		objs = append(objs, objAt(end.AddDate(0, 0, -i), backup.KindScheduled))
	}
	// Verified objects claiming later times on the same day as keep, and
	// unverified ones dated anywhere, cannot push keep out either.
	objs = append(objs,
		objAt(oldest.Header.CreatedAt.Add(time.Hour), backup.KindScheduled),
		unverifiedAt(end.Add(-time.Minute), backup.KindScheduled),
	)

	for _, r := range []backup.Retention{{}, {Daily: 1}, {Daily: 2, Weekly: 1, Monthly: 1}} {
		remove := backup.Prune(objs, r, time.UTC, oldest.Name, end)
		if slices.Contains(remove, oldest.Name) {
			t.Errorf("retention %+v: remove = %v, names keep %s", r, remove, oldest.Name)
		}
		if !slices.Contains(remove, backup.ObjectName(end.AddDate(0, 0, -3), backup.KindScheduled)) {
			t.Errorf("retention %+v: remove = %v, want the other old backups pruned as usual", r, remove)
		}
	}
}

func TestPruneIgnoresUnverified(t *testing.T) {
	end := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	tiers := []struct {
		name string
		r    backup.Retention
		step func(i int) time.Time // i tier units before end
	}{
		{"daily", backup.Retention{Daily: 2}, func(i int) time.Time { return end.AddDate(0, 0, -i) }},
		{"weekly", backup.Retention{Weekly: 2}, func(i int) time.Time { return end.AddDate(0, 0, -7*i) }},
		{"monthly", backup.Retention{Monthly: 2}, func(i int) time.Time { return end.AddDate(0, -i, 0) }},
	}
	for _, tc := range tiers {
		t.Run(tc.name, func(t *testing.T) {
			objs := []backup.Object{
				objAt(tc.step(1), backup.KindScheduled),
				objAt(tc.step(2), backup.KindScheduled),
				objAt(tc.step(3), backup.KindScheduled),
				// Newer than every verified object: in a bucket of its own,
				// and later within the newest verified object's bucket.
				unverifiedAt(tc.step(0), backup.KindScheduled),
				unverifiedAt(tc.step(1).Add(time.Hour), backup.KindScheduled),
				// Old enough to be pruned if it counted.
				unverifiedAt(tc.step(5), backup.KindScheduled),
			}
			remove := backup.Prune(objs, tc.r, time.UTC, "", end)
			want := []string{backup.ObjectName(tc.step(3), backup.KindScheduled)}
			if !slices.Equal(remove, want) {
				t.Errorf("remove = %v, want %v", remove, want)
			}
		})
	}
}

func TestPruneIgnoresFutureCreatedAt(t *testing.T) {
	now := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	future := []backup.Object{
		objAt(now.Add(time.Hour), backup.KindScheduled), // later today
		objAt(now.AddDate(0, 0, 1), backup.KindScheduled),
		objAt(now.AddDate(5, 0, 0), backup.KindScheduled),
	}
	objs := append([]backup.Object{
		objAt(now, backup.KindScheduled), // exactly now still counts
		objAt(now.AddDate(0, 0, -1), backup.KindScheduled),
		objAt(now.AddDate(0, 0, -2), backup.KindScheduled),
		objAt(now.AddDate(0, 0, -3), backup.KindScheduled),
	}, future...)

	remove := backup.Prune(objs, backup.Retention{Daily: 2}, time.UTC, "", now)

	want := []string{
		backup.ObjectName(now.AddDate(0, 0, -2), backup.KindScheduled),
		backup.ObjectName(now.AddDate(0, 0, -3), backup.KindScheduled),
	}
	if !slices.Equal(remove, want) {
		t.Errorf("remove = %v, want %v", remove, want)
	}
	for _, f := range future {
		if slices.Contains(remove, f.Name) {
			t.Errorf("remove = %v, names the future object %s", remove, f.Name)
		}
	}
}
