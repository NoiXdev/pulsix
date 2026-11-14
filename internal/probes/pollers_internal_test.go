package probes

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// snmpHarness is a harness with one snmp check on its host whose secret
// is stored.
func snmpHarness(t *testing.T, edit func(*harness, *Deps)) *harness {
	t.Helper()
	h := newHarness(t, edit)
	h.secrets.set(h.org, "core-sw", swFields)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		snmpCheck("sw", "core-sw", time.Minute))
	return h
}

// TestPollerKeptBetweenPolls: a slot's polls all go to one poller, so
// what one poll keeps (the counters a rate needs) reaches the next.
func TestPollerKeptBetweenPolls(t *testing.T) {
	h := snmpHarness(t, nil)
	for at := time.Duration(0); at <= 3*time.Minute; at += time.Minute {
		h.tickAt(at)
	}
	if got := h.pollers.polls(); !slices.Equal(got, []string{"1:sw", "1:sw", "1:sw", "1:sw"}) {
		t.Fatalf("polls = %v, want four by the one poller", got)
	}
}

// TestEachSlotHasItsOwnPoller: two snmp checks never share a poller.
func TestEachSlotHasItsOwnPoller(t *testing.T) {
	h := snmpHarness(t, nil)
	other := uuid.Must(uuid.NewV7())
	h.world.addHost(h.org, hosts.Host{ID: other, Address: "192.0.2.2", Monitored: true},
		snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(0)
	h.tickAt(time.Minute)
	ps := h.pollers.Built()
	if len(ps) != 2 {
		t.Fatalf("built %d pollers for two slots, want 2", len(ps))
	}
	for _, p := range ps {
		c := p.Calls()
		if len(c) != 2 || c[0].address != c[1].address {
			t.Fatalf("poller %d polled %+v, want one slot twice", p.id, c)
		}
	}
}

// TestPollerDroppedWhenTheSlotGoes: a slot that leaves the targets takes
// its poller with it; when it comes back it starts over.
func TestPollerDroppedWhenTheSlotGoes(t *testing.T) {
	h := snmpHarness(t, nil)
	h.tickAt(0)
	h.world.setChecks(h.host)
	h.tickAt(time.Minute)
	if n := len(h.r.pollers); n != 0 {
		t.Fatalf("%d pollers kept after the slot went", n)
	}
	h.world.setChecks(h.host, snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(2 * time.Minute)
	if got := h.pollers.polls(); !slices.Equal(got, []string{"1:sw", "2:sw"}) {
		t.Fatalf("polls = %v, want the returning slot on a new poller", got)
	}
}

// TestPollerDroppedWhenTheHostStopsBeingMonitored: same for a host that
// leaves monitoring.
func TestPollerDroppedWhenTheHostStopsBeingMonitored(t *testing.T) {
	h := snmpHarness(t, nil)
	h.tickAt(0)
	h.world.setMonitored(h.org, h.host, false)
	h.tickAt(time.Minute)
	if n := len(h.r.pollers); n != 0 {
		t.Fatalf("%d pollers kept for an unmonitored host", n)
	}
}

// TestPollerReplacedWhenTheParamsChange: a changed check starts over on
// a new poller; what the old one kept was measured with other params.
func TestPollerReplacedWhenTheParamsChange(t *testing.T) {
	h := snmpHarness(t, nil)
	h.tickAt(0)
	changed := snmpCheck("sw", "core-sw", time.Minute)
	changed.Params["interfaces"] = true
	h.world.setChecks(h.host, changed)
	h.tickAt(30 * time.Second)
	h.tickAt(90 * time.Second)
	if got := h.pollers.polls(); !slices.Equal(got, []string{"1:sw", "2:sw", "2:sw"}) {
		t.Fatalf("polls = %v, want the changed check on a new poller from its first poll on", got)
	}
}

// TestPollersDroppedWhenTheLockIsLost: another manager polls meanwhile,
// so what this one kept is stale; a regained lock starts over.
func TestPollersDroppedWhenTheLockIsLost(t *testing.T) {
	h := snmpHarness(t, nil)
	h.tickAt(0)
	h.locker.allow = false
	h.tickAt(time.Minute)
	if n := len(h.r.pollers); n != 0 {
		t.Fatalf("%d pollers kept after the lock was lost", n)
	}
	h.locker.allow = true
	h.tickAt(2 * time.Minute)
	if got := h.pollers.polls(); !slices.Equal(got, []string{"1:sw", "2:sw"}) {
		t.Fatalf("polls = %v, want a new poller after the lock came back", got)
	}
}

// TestStatelessPluginHasNoPoller: other plugins run through the probe
// function and keep nothing.
func TestStatelessPluginHasNoPoller(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.tickAt(0)
	if n := len(h.pollers.Built()); n != 0 || len(h.r.pollers) != 0 {
		t.Fatalf("built %d pollers for an http check", n)
	}
	if n := len(h.probe.Calls()); n != 1 {
		t.Fatalf("probed %d times, want once", n)
	}
}

// TestDefaultPollerIsSNMPs: without NewPoller the runner polls snmp
// slots with snmp's Poller.
func TestDefaultPollerIsSNMPs(t *testing.T) {
	r := NewRunner(Deps{})
	if _, ok := r.newPoller().(snmpPoller); !ok {
		t.Fatalf("default poller is %T, want snmpPoller", r.newPoller())
	}
}

// TestSkippedInterfacesLoggedOncePerHour: a device with more than 128
// interfaces is logged at most once per check per hour, by the runner's
// clock; another check logs on its own.
func TestSkippedInterfacesLoggedOncePerHour(t *testing.T) {
	var logs syncBuffer
	h := snmpHarness(t, func(h *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		h.pollers.skipped = true
	})
	count := func() int { return strings.Count(logs.String(), msgInterfacesSkipped) }
	for at := time.Duration(0); at < time.Hour; at += time.Minute {
		h.tickAt(at)
	}
	if n := count(); n != 1 {
		t.Fatalf("logged %d times in the first hour, want once", n)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "host="+h.host.String()) ||
		!strings.Contains(out, "check=sw") || !strings.Contains(out, "org="+h.org.String()) {
		t.Fatalf("log %q lacks the level or the check's org, host and key", out)
	}

	other := uuid.Must(uuid.NewV7())
	h.world.addHost(h.org, hosts.Host{ID: other, Address: "192.0.2.2", Monitored: true},
		snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(time.Hour - 30*time.Second)
	if n := count(); n != 2 {
		t.Fatalf("logged %d times, want the other check's first skip logged", n)
	}
	h.tickAt(time.Hour)
	if n := count(); n != 3 {
		t.Fatalf("logged %d times, want the first check logged again an hour later", n)
	}
}

// TestSkipLogSurvivesASlotsReturn: a check that leaves and comes back
// within the hour is not logged again.
func TestSkipLogSurvivesASlotsReturn(t *testing.T) {
	var logs syncBuffer
	h := snmpHarness(t, func(h *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		h.pollers.skipped = true
	})
	h.tickAt(0)
	h.world.setChecks(h.host)
	h.tickAt(time.Minute)
	h.world.setChecks(h.host, snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(2 * time.Minute)
	if n := strings.Count(logs.String(), msgInterfacesSkipped); n != 1 {
		t.Fatalf("logged %d times, want once within the hour", n)
	}
}

// TestThrottleForgetsOldEntries: entries older than the period are
// dropped, so the map does not grow with every check ever skipped.
func TestThrottleForgetsOldEntries(t *testing.T) {
	th := newThrottle(time.Hour)
	a, b := slot{key: "a"}, slot{key: "b"}
	if !th.allow(a, t0) || th.allow(a, t0.Add(59*time.Minute)) {
		t.Fatal("a: want allowed once within the hour")
	}
	if !th.allow(b, t0.Add(2*time.Hour)) {
		t.Fatal("b: want allowed")
	}
	if _, ok := th.last[a]; ok {
		t.Fatal("a's entry outlived its hour")
	}
	if !th.allow(a, t0.Add(2*time.Hour)) {
		t.Fatal("a: want allowed again after the hour")
	}
}
