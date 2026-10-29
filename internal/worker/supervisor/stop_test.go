package supervisor

import (
	"errors"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reaction scripts how a fake process answers signals.
type reaction struct {
	diesOnTerm  bool // SIGTERM removes the process
	spawnOnTerm int  // SIGTERM first forks this pid (0 = none)
	immortal    bool // nothing, SIGKILL included, removes the process
}

type sent struct {
	pid int
	sig syscall.Signal
	at  time.Time
}

type fakeClock struct {
	now    time.Time
	sleeps int
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(d time.Duration) {
	c.sleeps++
	c.now = c.now.Add(d)
}

// fakeTable is a procTable over a scripted set of live pids. Signalling a
// pid that is not live is a recorded-nowhere no-op, as the interface demands.
type fakeTable struct {
	clk     *fakeClock
	live    map[int]bool
	react   map[int]reaction
	sent    []sent
	listErr error
}

func newFakeTable(clk *fakeClock, react map[int]reaction) *fakeTable {
	ft := &fakeTable{clk: clk, live: map[int]bool{}, react: react}
	for pid := range react {
		ft.live[pid] = true
	}
	return ft
}

func (ft *fakeTable) Descendants() ([]int, error) {
	if ft.listErr != nil {
		return nil, ft.listErr
	}
	pids := make([]int, 0, len(ft.live))
	for pid := range ft.live {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func (ft *fakeTable) Signal(pid int, sig syscall.Signal) error {
	if !ft.live[pid] {
		return nil
	}
	ft.sent = append(ft.sent, sent{pid, sig, ft.clk.now})
	r := ft.react[pid]
	switch {
	case r.immortal:
	case sig == syscall.SIGKILL:
		delete(ft.live, pid)
	case sig == syscall.SIGTERM:
		if r.spawnOnTerm != 0 {
			ft.live[r.spawnOnTerm] = true
			if _, ok := ft.react[r.spawnOnTerm]; !ok {
				ft.react[r.spawnOnTerm] = reaction{diesOnTerm: true}
			}
		}
		if r.diesOnTerm {
			delete(ft.live, pid)
		}
	}
	return nil
}

// count returns how many times sig was sent to pid (pid 0 = any pid).
func (ft *fakeTable) count(pid int, sig syscall.Signal) int {
	n := 0
	for _, s := range ft.sent {
		if (pid == 0 || s.pid == pid) && s.sig == sig {
			n++
		}
	}
	return n
}

const (
	testGrace = 5 * time.Second
	testPoll  = 500 * time.Millisecond
)

func TestStopTreeEmptyReturnsAtOnce(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ft := newFakeTable(clk, nil)

	if err := stopTree(ft, clk, testGrace, testPoll, 3); err != nil {
		t.Fatalf("stopTree: %v", err)
	}
	if clk.sleeps != 0 {
		t.Errorf("slept %d times on an empty tree, want 0", clk.sleeps)
	}
	if len(ft.sent) != 0 {
		t.Errorf("sent %d signals on an empty tree, want 0", len(ft.sent))
	}
}

func TestStopTreeTermsEveryDescendantOnce(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ft := newFakeTable(clk, map[int]reaction{
		10: {diesOnTerm: true}, 11: {diesOnTerm: true}, 12: {diesOnTerm: true},
	})

	if err := stopTree(ft, clk, testGrace, testPoll, 3); err != nil {
		t.Fatalf("stopTree: %v", err)
	}
	for _, pid := range []int{10, 11, 12} {
		if n := ft.count(pid, syscall.SIGTERM); n != 1 {
			t.Errorf("pid %d got %d SIGTERMs, want 1", pid, n)
		}
	}
	if n := ft.count(0, syscall.SIGKILL); n != 0 {
		t.Errorf("sent %d SIGKILLs to a tree that obeyed SIGTERM, want 0", n)
	}
}

func TestStopTreeTermsLateForks(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ft := newFakeTable(clk, map[int]reaction{
		10: {diesOnTerm: true, spawnOnTerm: 20},
	})

	if err := stopTree(ft, clk, testGrace, testPoll, 3); err != nil {
		t.Fatalf("stopTree: %v", err)
	}
	for _, pid := range []int{10, 20} {
		if n := ft.count(pid, syscall.SIGTERM); n != 1 {
			t.Errorf("pid %d got %d SIGTERMs, want 1", pid, n)
		}
	}
	if n := ft.count(0, syscall.SIGKILL); n != 0 {
		t.Errorf("sent %d SIGKILLs, want 0", n)
	}
}

func TestStopTreeKillsAfterGrace(t *testing.T) {
	start := time.Unix(1000, 0)
	clk := &fakeClock{now: start}
	ft := newFakeTable(clk, map[int]reaction{10: {}}) // ignores TERM, dies on KILL

	if err := stopTree(ft, clk, testGrace, testPoll, 3); err != nil {
		t.Fatalf("stopTree: %v", err)
	}
	if n := ft.count(10, syscall.SIGTERM); n != 1 {
		t.Errorf("got %d SIGTERMs, want exactly 1 (a TERMed pid is not TERMed again)", n)
	}
	if n := ft.count(10, syscall.SIGKILL); n != 1 {
		t.Fatalf("got %d SIGKILLs, want 1", n)
	}
	for _, s := range ft.sent {
		if s.sig == syscall.SIGKILL && s.at.Sub(start) < testGrace {
			t.Errorf("SIGKILL sent %v after the start, before the %v grace elapsed", s.at.Sub(start), testGrace)
		}
	}
}

func TestStopTreeGivesUp(t *testing.T) {
	const passes = 4
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ft := newFakeTable(clk, map[int]reaction{10: {immortal: true}})

	err := stopTree(ft, clk, testGrace, testPoll, passes)
	if !errors.Is(err, ErrTreeDidNotDrain) {
		t.Fatalf("err = %v, want ErrTreeDidNotDrain", err)
	}
	if n := ft.count(10, syscall.SIGKILL); n != passes {
		t.Errorf("got %d SIGKILLs, want exactly %d", n, passes)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("err = %q, want it to name the surviving pid 10", err)
	}
}

func TestStopTreeDescendantsErrorIsReturned(t *testing.T) {
	boom := errors.New("proc unreadable")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ft := newFakeTable(clk, nil)
	ft.listErr = boom

	err := stopTree(ft, clk, testGrace, testPoll, 3)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if errors.Is(err, ErrTreeDidNotDrain) {
		t.Errorf("a listing failure must not read as a tree that would not drain")
	}
}

func TestStopTreeFailedTermStillLeadsToKill(t *testing.T) {
	boom := errors.New("pidfd refused")
	start := time.Unix(1000, 0)
	clk := &fakeClock{now: start}
	// pid 10 ignores TERM (and the TERM errors out); pid 11 obeys it.
	inner := newFakeTable(clk, map[int]reaction{10: {}, 11: {diesOnTerm: true}})
	ft := &failingSignal{fakeTable: inner, failPid: 10, failSig: syscall.SIGTERM, err: boom}

	err := stopTree(ft, clk, testGrace, testPoll, 3)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to contain %v", err, boom)
	}
	if errors.Is(err, ErrTreeDidNotDrain) {
		t.Errorf("err = %v: the KILL phase drained the tree, it must not read as undrained", err)
	}
	if !strings.Contains(err.Error(), "signal 10 with terminated") {
		t.Errorf("err = %q, want the signal error wrapped with its pid and signal", err)
	}
	if n := inner.count(11, syscall.SIGTERM); n != 1 {
		t.Errorf("pid 11 got %d SIGTERMs, want 1: one failing signal must not spare the others", n)
	}
	if n := inner.count(11, syscall.SIGKILL); n != 0 {
		t.Errorf("pid 11 got %d SIGKILLs, want 0 (it obeyed TERM)", n)
	}
	if n := inner.count(10, syscall.SIGKILL); n != 1 {
		t.Fatalf("pid 10 got %d SIGKILLs, want 1: a failed TERM must not skip the KILL", n)
	}
	for _, s := range inner.sent {
		if s.sig == syscall.SIGKILL && s.at.Sub(start) < testGrace {
			t.Errorf("SIGKILL sent %v after the start, before the %v grace elapsed", s.at.Sub(start), testGrace)
		}
	}
}

func TestStopTreeUnsignalableSurvivorReportsBoth(t *testing.T) {
	const passes = 3
	boom := errors.New("pidfd refused")
	clk := &fakeClock{now: time.Unix(1000, 0)}
	inner := newFakeTable(clk, map[int]reaction{10: {}})
	ft := &failingSignal{fakeTable: inner, failPid: 10, err: boom} // every signal to 10 fails

	err := stopTree(ft, clk, testGrace, testPoll, passes)
	if !errors.Is(err, boom) || !errors.Is(err, ErrTreeDidNotDrain) {
		t.Fatalf("err = %v, want both the signal error and ErrTreeDidNotDrain", err)
	}
	if ft.kills != passes {
		t.Errorf("attempted %d SIGKILLs, want %d: failing signals must not shorten the kill phase", ft.kills, passes)
	}
}

// failingSignal fails signals to failPid (only failSig when set, else every
// signal) and counts the SIGKILLs it was asked to send.
type failingSignal struct {
	*fakeTable
	failPid int
	failSig syscall.Signal
	err     error
	kills   int
}

func (f *failingSignal) Signal(pid int, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		f.kills++
	}
	if pid == f.failPid && (f.failSig == 0 || f.failSig == sig) {
		return f.err
	}
	return f.fakeTable.Signal(pid, sig)
}
