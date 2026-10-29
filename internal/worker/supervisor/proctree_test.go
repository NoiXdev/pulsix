package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
)

// fakeProc is a scripted /proc: pid → stat line, or pid → read error.
type fakeProc struct {
	stat  map[int]string
	fails map[int]error
	reads []int
}

func (p *fakeProc) read(pid int) ([]byte, error) {
	p.reads = append(p.reads, pid)
	if err, ok := p.fails[pid]; ok {
		return nil, err
	}
	s, ok := p.stat[pid]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

func (p *fakeProc) pids() []int {
	var out []int
	for pid := range p.stat {
		out = append(out, pid)
	}
	for pid := range p.fails {
		out = append(out, pid)
	}
	slices.Sort(out)
	return out
}

// stat builds a stat line for pid with the given state and parent.
func stat(pid int, state string, ppid int) string {
	return fmt.Sprintf("%d (x) %s %d %d %d 0", pid, state, ppid, pid, pid)
}

// A supervisor (100) with a child runner (101), the runner's child (102), an
// orphan reparented to the supervisor in another session (103), and
// unrelated processes around it.
func newTree() *fakeProc {
	return &fakeProc{stat: map[int]string{
		1:   stat(1, "S", 0),
		50:  stat(50, "S", 1),
		100: stat(100, "S", 50),
		101: stat(101, "S", 100),
		102: stat(102, "R", 101),
		103: stat(103, "S", 100),
		200: stat(200, "S", 1),
		201: stat(201, "S", 200),
	}}
}

func TestDescendantsFrom(t *testing.T) {
	p := newTree()
	got := descendantsFrom(100, p.pids(), p.read)
	slices.Sort(got)
	if want := []int{101, 102, 103}; !slices.Equal(got, want) {
		t.Errorf("descendants = %v, want %v", got, want)
	}
}

func TestDescendantsFromSkipsZombiesAndDead(t *testing.T) {
	p := newTree()
	p.stat[102] = stat(102, "Z", 101)
	p.stat[103] = stat(103, "X", 100)
	got := descendantsFrom(100, p.pids(), p.read)
	if want := []int{101}; !slices.Equal(got, want) {
		t.Errorf("descendants = %v, want %v", got, want)
	}
}

func TestDescendantsFromSkipsUnreadableEntries(t *testing.T) {
	p := newTree()
	// Unrelated entries this uid may not read, one that vanished, and one
	// that does not parse: none of them may cost the supervisor its tree.
	p.fails = map[int]error{
		300: fs.ErrPermission,
		301: errors.New("input/output error"),
	}
	p.stat[302] = "garbage"
	p.stat[303] = "303 (x) S notapid 1 1 0"
	got := descendantsFrom(100, p.pids(), p.read)
	slices.Sort(got)
	if want := []int{101, 102, 103}; !slices.Equal(got, want) {
		t.Errorf("descendants = %v, want %v despite unreadable unrelated entries", got, want)
	}
}

func TestIsDescendant(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		want bool
	}{
		{"direct child", 101, true},
		{"grandchild", 102, true},
		{"orphan reparented to the supervisor", 103, true},
		{"the supervisor itself", 100, false},
		{"its parent", 50, false},
		{"PID 1", 1, false},
		{"an unrelated process", 201, false},
		{"a pid that does not exist", 999, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTree()
			if got := isDescendant(100, tc.pid, p.read); got != tc.want {
				t.Errorf("isDescendant(100, %d) = %v, want %v", tc.pid, got, tc.want)
			}
		})
	}
}

func TestIsDescendantReadsOnlyTheChain(t *testing.T) {
	p := newTree()
	if !isDescendant(100, 102, p.read) {
		t.Fatal("isDescendant(100, 102) = false, want true")
	}
	if want := []int{102, 101}; !slices.Equal(p.reads, want) {
		t.Errorf("read %v, want only the chain %v", p.reads, want)
	}
}

func TestIsDescendantRefusesAnUnreadableLink(t *testing.T) {
	p := newTree()
	p.fails = map[int]error{101: fs.ErrPermission}
	if isDescendant(100, 102, p.read) {
		t.Error("isDescendant through an unreadable link = true, want false: an unproven pid is left alone")
	}
}

func TestIsDescendantStopsOnALoop(t *testing.T) {
	// The kernel's chain cannot loop, but a table read while it changes
	// must still not walk forever.
	p := &fakeProc{stat: map[int]string{7: stat(7, "S", 8), 8: stat(8, "S", 7)}}
	if isDescendant(100, 7, p.read) {
		t.Error("isDescendant on a loop = true, want false")
	}
	if len(p.reads) != maxChainHops {
		t.Errorf("read %d links, want the walk bounded at %d", len(p.reads), maxChainHops)
	}
}

func TestParseStat(t *testing.T) {
	tests := []struct {
		name  string
		stat  string
		state string
		ppid  string
	}{
		{"plain", "42 (sleep) S 7 42 42 0 -1", "S", "7"},
		{"comm with a paren and spaces", "42 (a) b) c) Z 9 1 1 0", "Z", "9"},
		{"comm with a newline", "42 (x\ny) R 3 1 1 0", "R", "3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := parseStat([]byte(tc.stat))
			if err != nil {
				t.Fatalf("parseStat: %v", err)
			}
			if fields[0] != tc.state || fields[1] != tc.ppid {
				t.Errorf("state, ppid = %q, %q, want %q, %q", fields[0], fields[1], tc.state, tc.ppid)
			}
		})
	}
	for _, bad := range []string{"", "42 sleep S 7", "42 (sleep)", "42 (sleep) S"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("parseStat(%q) succeeded, want an error", bad)
		}
	}
}
