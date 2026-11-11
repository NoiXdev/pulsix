package probes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
)

// selectiveProbe blocks the probes whose key starts with "a" until block
// is closed, one value on release frees one of them, or their ctx ends;
// it answers every other one at once, reporting its key on answered.
type selectiveProbe struct {
	block    chan struct{}
	release  chan struct{}
	answered chan string
	mu       sync.Mutex
	keys     []string
}

func (p *selectiveProbe) Run(ctx context.Context, plugin string, params map[string]any, _ string) []probe.Observation {
	key, _ := params["key"].(string)
	p.mu.Lock()
	p.keys = append(p.keys, key)
	p.mu.Unlock()
	if strings.HasPrefix(key, "a") {
		select {
		case <-p.block:
		case <-p.release:
		case <-ctx.Done():
		}
	} else {
		p.answered <- key
	}
	one := 1.0
	return []probe.Observation{{Metric: plugin + ".up", Value: &one}}
}

func (p *selectiveProbe) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, k := range p.keys {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// twoOrgs is a harness with a second organization, orgB.
func twoOrgs(t *testing.T, p ProbeFunc) (*harness, uuid.UUID) {
	t.Helper()
	orgB := uuid.Must(uuid.NewV7())
	h := newHarness(t, func(hh *harness, d *Deps) {
		d.Orgs = fakeOrgs{ids: []uuid.UUID{hh.org, orgB}}
		if p != nil {
			d.Probe = p
		}
	})
	return h, orgB
}

func (h *harness) addChecks(org uuid.UUID, prefix string, n int, interval time.Duration) {
	for i := 0; i < n; i++ {
		h.world.addHost(org, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true},
			synthetic(fmt.Sprintf("%s%d", prefix, i), "tcp", interval))
	}
}

// TestOrgCannotStarveAnother: org A's black-hole probes, however many,
// cannot keep org B's due probe from starting — at the same tick, and,
// when B falls due after A (alone with due work) took every slot, as soon
// as one of A's slots frees: A does not get it back while B waits (final
// review, Important #5).
func TestOrgCannotStarveAnother(t *testing.T) {
	sp := &selectiveProbe{block: make(chan struct{}), release: make(chan struct{}), answered: make(chan string, 8)}
	h, orgB := twoOrgs(t, sp.Run)
	h.addChecks(h.org, "a", 40, time.Minute)
	h.addChecks(orgB, "b", 1, time.Minute)
	ctx := context.Background()
	defer func() {
		close(sp.block)
		h.r.wg.Wait()
	}()

	// started reports whether org B's probe ran.
	started := func() bool {
		select {
		case <-sp.answered:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
	h.clock.Set(t0)
	h.r.tick(ctx)
	if !started() {
		t.Fatal("org B was not probed at t=0")
	}
	// B has nothing due until t=60 s, so A may use every slot meanwhile.
	for at := 5 * time.Second; at < time.Minute; at += 5 * time.Second {
		h.clock.Set(t0.Add(at))
		h.r.tick(ctx)
	}
	if n := h.running(); n != DefaultMaxConcurrent {
		t.Fatalf("%d probes in flight while only A had due work, want all %d slots used", n, DefaultMaxConcurrent)
	}
	h.clock.Set(t0.Add(time.Minute))
	h.r.tick(ctx) // B is due; no slot is free
	aBefore := sp.count("a")

	// One of A's probes ends; its completion launches again.
	sp.release <- struct{}{}
	waitFor(t, func() bool { return h.running() == DefaultMaxConcurrent-1 })
	h.r.launchFreed(ctx)
	if !started() {
		t.Fatal("org B was not probed when a slot freed while A's probes hung")
	}
	if n := sp.count("a"); n != aBefore {
		t.Fatalf("A started %d more probes into the freed slot while B waited", n-aBefore)
	}
	if n := sp.count("b"); n != 2 {
		t.Fatalf("org B was probed %d times, want twice", n)
	}
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestIdleOrgsReserveNothing: organizations with checks but nothing due
// hold no slots back, so a busy organization's due probes fill all 32 at
// once however many idle organizations there are (re-review of the fair
// share: a reserve per organization with checks capped a busy one at 16,
// 8 … 1 launches per tick).
func TestIdleOrgsReserveNothing(t *testing.T) {
	for _, idle := range []int{0, 1, 3, 10, 31, 40} {
		t.Run(fmt.Sprint(idle), func(t *testing.T) {
			others := make([]uuid.UUID, idle)
			for i := range others {
				others[i] = uuid.Must(uuid.NewV7())
			}
			h := newHarness(t, func(hh *harness, d *Deps) {
				d.Orgs = fakeOrgs{ids: append([]uuid.UUID{hh.org}, others...)}
			})
			for _, o := range others {
				h.addChecks(o, "z", 4, time.Hour)
			}
			// Every idle organization's checks run once and are then not
			// due for an hour.
			for at := time.Duration(0); at < 30*time.Second; at += time.Second {
				h.tickAt(at)
			}
			if n := len(h.probe.Keys()); n != 4*idle {
				t.Fatalf("idle organizations probed %d times, want %d", n, 4*idle)
			}
			h.probe.mu.Lock()
			h.probe.block = make(chan struct{})
			h.probe.mu.Unlock()
			h.addChecks(h.org, "a", 100, time.Minute)
			h.clock.Set(t0.Add(time.Minute)) // refresh; all 100 due
			h.r.tick(context.Background())
			n := h.running()
			h.probe.mu.Lock()
			close(h.probe.block)
			h.probe.mu.Unlock()
			h.r.wg.Wait()
			if n != DefaultMaxConcurrent {
				t.Fatalf("the busy organization started %d probes with all slots free, want %d", n, DefaultMaxConcurrent)
			}
		})
	}
}

// TestCompletionLaunchesAgain: a finished probe frees its slot for a due
// one at once, not on the next tick, so the bound limits concurrency, not
// launches per tick.
func TestCompletionLaunchesAgain(t *testing.T) {
	h := newHarness(t, func(_ *harness, d *Deps) {
		d.Tick = time.Minute // only Run's first tick within the test
		d.MaxConcurrent = 2
	})
	for i := 0; i < 7; i++ {
		h.world.addHost(h.org, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true},
			synthetic(fmt.Sprintf("c%d", i), "tcp", time.Hour))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, func() bool { return len(h.probe.Keys()) == 7 })
	if m := h.probe.MaxInFlight(); m > 2 {
		t.Fatalf("max in flight = %d, want at most 2", m)
	}
	if got := sorted(h.probe.Keys()); !slices.Equal(got, []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6"}) {
		t.Fatalf("probed %v, want each check once", got)
	}
}

// TestNoLaunchOnCompletionWithoutTheLock: a completion never launches
// while the lock is not held.
func TestNoLaunchOnCompletionWithoutTheLock(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.locker.allow = false
	h.tickAt(0)
	h.r.launchFreed(context.Background())
	h.r.wg.Wait()
	if n := len(h.probe.Keys()); n != 0 {
		t.Fatalf("probed %d times without the lock", n)
	}
}

// TestBusyOrgsShareTheSlots: two organizations with more due work than
// slots get half the slots each.
func TestBusyOrgsShareTheSlots(t *testing.T) {
	h, orgB := twoOrgs(t, nil)
	h.probe.block = make(chan struct{})
	h.addChecks(h.org, "a", 40, time.Minute)
	h.addChecks(orgB, "b", 40, time.Minute)
	h.clock.Set(t0)
	h.r.tick(context.Background())
	perOrg := map[uuid.UUID]int{}
	h.r.mu.Lock()
	for s := range h.r.inFlight {
		perOrg[h.r.targets[s].org]++
	}
	h.r.mu.Unlock()
	close(h.probe.block)
	h.r.wg.Wait()
	if perOrg[h.org] != 16 || perOrg[orgB] != 16 {
		t.Fatalf("in flight per org = %v, want 16 each", perOrg)
	}
}

// TestLoneOrgUsesEverySlot: with no other organization to keep room
// for, one organization may use all the slots.
func TestLoneOrgUsesEverySlot(t *testing.T) {
	h, orgB := twoOrgs(t, nil)
	h.probe.block = make(chan struct{})
	h.addChecks(h.org, "a", 40, time.Minute)
	h.world.addHost(orgB, hosts.Host{ID: uuid.Must(uuid.NewV7()), Address: "192.0.2.1", Monitored: true})
	h.clock.Set(t0)
	h.r.tick(context.Background())
	n := h.running()
	close(h.probe.block)
	h.r.wg.Wait()
	if n != DefaultMaxConcurrent {
		t.Fatalf("started %d probes, want all %d slots for the only organization with checks", n, DefaultMaxConcurrent)
	}
}
