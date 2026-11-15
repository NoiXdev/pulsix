package zfs

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantNum is a number's expected outcome: a value, an error or absent.
type wantNum struct {
	value  float64
	err    bool
	absent bool
}

func num(v float64) wantNum { return wantNum{value: v} }

var (
	numErr    = wantNum{err: true}
	numAbsent = wantNum{absent: true}
)

func checkNum(t *testing.T, what string, got number, want wantNum) {
	t.Helper()
	switch {
	case want.err:
		if got.err == nil {
			t.Errorf("%s = %+v, want an error", what, got)
		}
	case want.absent:
		if !got.absent || got.err != nil {
			t.Errorf("%s = %+v, want absent", what, got)
		}
	default:
		if got.err != nil || got.absent || got.value != want.value {
			t.Errorf("%s = %+v, want %v", what, got, want.value)
		}
	}
}

func TestParseList(t *testing.T) {
	type pool struct {
		name, health                    string
		size, free, capacity, fragments wantNum
	}
	for _, tc := range []struct {
		file string
		want []pool
	}{
		{"list_one.txt", []pool{
			{"tank", "ONLINE", num(3985729650688), num(3201863843840), num(19), num(2)},
		}},
		{"list_two_pools.txt", []pool{
			{"rpool", "ONLINE", num(498216206336), num(421380136960), num(15), num(7)},
			{"tank", "ONLINE", num(11991548690432), num(7140594565120), num(40), num(11)},
		}},
		// Older releases print the percentages with a '%'; a pool without
		// the space map histogram feature prints '-' for fragmentation.
		{"list_old.txt", []pool{
			{"tank", "ONLINE", num(3985729650688), num(3201863843840), num(19), num(2)},
			{"backup", "DEGRADED", num(1992864825344), num(996432412672), num(50), numAbsent},
		}},
		// An unavailable pool prints '-' for every property but its name
		// and health.
		{"list_unavail.txt", []pool{
			{"tank", "ONLINE", num(3985729650688), num(3201863843840), num(19), num(2)},
			{"backup", "UNAVAIL", numErr, numErr, numErr, numAbsent},
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got, err := parseList(load(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d pools, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.name != w.name || g.health != w.health {
					t.Errorf("pool %d = %q %q, want %q %q", i, g.name, g.health, w.name, w.health)
				}
				checkNum(t, w.name+" size", g.size, w.size)
				checkNum(t, w.name+" free", g.free, w.free)
				checkNum(t, w.name+" capacity", g.capacity, w.capacity)
				checkNum(t, w.name+" fragmentation", g.fragmentation, w.fragments)
			}
		})
	}
}

func TestParseListEmpty(t *testing.T) {
	for _, in := range []string{"", "\n", "\n\n"} {
		got, err := parseList([]byte(in))
		if err != nil || len(got) != 0 {
			t.Errorf("parseList(%q) = %+v, %v", in, got, err)
		}
	}
}

// TestParseListBadNumbers: a value that does not parse is that property's
// error, not the whole list's.
func TestParseListBadNumbers(t *testing.T) {
	got, err := parseList([]byte("tank\tONLINE\t12x\t-5\t1e400\tabc%\n"))
	if err != nil || len(got) != 1 {
		t.Fatalf("parseList = %+v, %v", got, err)
	}
	checkNum(t, "size", got[0].size, numErr)
	checkNum(t, "free", got[0].free, numErr)
	checkNum(t, "capacity", got[0].capacity, numErr)
	checkNum(t, "fragmentation", got[0].fragmentation, numErr)
}

// TestParseListBadLine: a line without the six columns asked for means the
// output is not what was asked for; the whole call fails.
func TestParseListBadLine(t *testing.T) {
	for _, in := range []string{
		"tank\tONLINE\t1\t2\t3\n",
		"tank ONLINE 1 2 3 4\n",
		"tank\tONLINE\t1\t2\t3\t4\t5\n",
		"\tONLINE\t1\t2\t3\t4\n",
	} {
		if got, err := parseList([]byte(in)); err == nil {
			t.Errorf("parseList(%q) = %+v, want an error", in, got)
		}
	}
}
