package zfs

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestOptionalPoolAbsentIsExported(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\nrpool\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{"optional_pools": []any{"backup"}})
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "backup", "zfs.state", "EXPORTED")
	n := 0
	for _, o := range obs {
		if o.Item == "backup" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("backup reported %d observations, want exactly zfs.state", n)
	}
	// An optional list does not narrow: every imported pool is reported.
	wantLabel(t, obs, "tank", "zfs.state", "ONLINE")
	wantLabel(t, obs, "rpool", "zfs.state", "ONLINE")
	wantValue(t, obs, "", "zfs.pools", 2)
	for _, c := range f.callList() {
		for _, a := range c {
			if a == "backup" {
				t.Errorf("call names the absent pool: %q", c)
			}
		}
	}
}

func TestListedPoolAbsentIsMissing(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{"pools": []any{"tank", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "nope", "zfs.state", "MISSING")
	n := 0
	for _, o := range obs {
		if o.Item == "nope" || o.Item == "rpool" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("nope/rpool observations = %d, want only nope's zfs.state", n)
	}
	wantLabel(t, obs, "tank", "zfs.state", "ONLINE")
	wantValue(t, obs, "", "zfs.pools", 1)
}

func TestOnlyAbsentPoolsSkipStatus(t *testing.T) {
	f := newFake(t, map[string]reply{"zpool list": {stdout: load(t, "list_two_pools.txt")}})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{
		"pools": []string{"nope"}, "optional_pools": []string{"gone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "nope", "zfs.state", "MISSING")
	wantLabel(t, obs, "gone", "zfs.state", "EXPORTED")
	wantValue(t, obs, "", "zfs.pools", 0)
	if len(obs) != 3 {
		t.Errorf("obs = %+v", obs)
	}
	if got := f.callList(); !slices.EqualFunc(got, [][]string{listArgs}, slices.Equal) {
		t.Errorf("calls = %q", got)
	}
}

func TestBothListsBothPresent(t *testing.T) {
	f := newFake(t, map[string]reply{
		"zpool list":   {stdout: load(t, "list_two_pools.txt")},
		"zpool status": {stdout: load(t, "status_two_pools.txt")},
		"zfs get":      {stdout: []byte("tank\t1717243200\n")},
	})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{
		"pools": []string{"tank"}, "optional_pools": []string{"rpool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "tank", "zfs.state", "ONLINE")
	wantLabel(t, obs, "rpool", "zfs.state", "ONLINE")
	wantValue(t, obs, "", "zfs.pools", 2)
	wantValue(t, obs, "rpool", "zfs.capacity_pct", 15)
}

func TestNameInBothListsAbsentIsExported(t *testing.T) {
	f := newFake(t, map[string]reply{"zpool list": {stdout: load(t, "list_two_pools.txt")}})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{
		"pools": []string{"nope"}, "optional_pools": []string{"nope"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLabel(t, obs, "nope", "zfs.state", "EXPORTED")
	if len(obs) != 2 {
		t.Errorf("obs = %+v", obs)
	}
}

func TestListFailureReportsNoAbsentPools(t *testing.T) {
	f := newFake(t, map[string]reply{"zpool list": {stderr: []byte("boom\n"), err: errors.New("exit status 1")}})
	obs, err := testPlugin(f).Collect(context.Background(), map[string]any{
		"pools": []string{"nope"}, "optional_pools": []string{"gone"},
	})
	if err == nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	wantErr(t, obs, "", "zfs.pools", "boom")
}

func TestOptionalPoolsBadParam(t *testing.T) {
	f := newFake(t, nil)
	if _, err := testPlugin(f).Collect(context.Background(), map[string]any{"optional_pools": 5}); err == nil {
		t.Fatal("want error")
	}
}
