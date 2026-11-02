// The play uid's sweep between jobs, proven in the image (backlogs 7.21
// and 7.35).
package uidboundary

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// TestAPlayThatKillsItsSupervisorDoesNotOutliveItsJob is backlog 7.21's
// process half. The supervisor runs as the play uid, so the play may kill
// it; a process the play detached first was then left running past the
// end of the job, waiting for the next one, since nothing else stops the
// play uid's processes. The loop now sweeps the play uid after every job:
// once the job has been reported and the loop has stopped, no live
// process of the play uid remains.
func TestAPlayThatKillsItsSupervisorDoesNotOutliveItsJob(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	t.Cleanup(func() { reapPlayUIDLeftovers(t) })

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: detach a process, then kill the supervisor
      ansible.builtin.shell: |
        setsid nohup sleep 307 >/dev/null 2>&1 </dev/null &
        # Spelled in two halves so this shell's own command line does not
        # match the supervisor's argument.
        pat="__pulsix-play-""supervisor"
        for p in /proc/[0-9]*; do
          pid=${p#/proc/}
          [ "$pid" = "$$" ] && continue
          if tr '\0' ' ' <"$p/cmdline" 2>/dev/null | grep -q -- "$pat"; then
            kill -9 "$pid"
          fi
        done
        sleep 1
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-0000000000d1"

	result, events, logs := runOneJob(t, job)
	t.Logf("status %q, error %q\n%s", result.GetStatus(), result.GetError(), eventDump(events))

	if pid, state, ok := processWithCmdline(t, "sleep 307"); ok && !strings.HasPrefix(state, "Z") && !strings.HasPrefix(state, "X") {
		t.Errorf("the detached sleep outlived its job: pid %d, state %q\n%s\n%s", pid, state, debugPlayUIDProcesses(t), logs)
	}
	if playUIDProcessExists(t) {
		t.Errorf("a live process of uid %d outlived the job:\n%s", playUID, debugPlayUIDProcesses(t))
	}
}

// sweepDisabledMarker, when it exists, makes this binary's sweep mode do
// nothing (TestMain): a leftover play process that kills the sweep before
// it acts, without the race.
const sweepDisabledMarker = "/tmp/pulsix-uidboundary-sweep-disabled"

// TestAWorkerWhoseSweepFailsStops is backlog 7.35. The sweep runs as the
// uid it clears, so a leftover can kill it first, and its exit status
// proves nothing. The worker checks the process table from its own uid
// afterwards: here the sweep does nothing, the detached process survives
// every attempt, and the loop must stop instead of claiming the next job
// next to it — the worker exits and its container's restart ends it.
func TestAWorkerWhoseSweepFailsStops(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	if err := os.WriteFile(sweepDisabledMarker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The survivor is expected here, so this sweeps it the worker's
		// way rather than through reapPlayUIDLeftovers, which reports one.
		_ = os.Remove(sweepDisabledMarker)
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(workerConfig(t).SetuidHelper, strconv.Itoa(playUID), self, supervisor.SweepArg).CombinedOutput(); err != nil {
			t.Errorf("sweep the survivor: %v: %s", err, out)
		}
	})

	job := localhostJob(t, `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: detach a process, then kill the supervisor
      ansible.builtin.shell: |
        setsid nohup sleep 311 >/dev/null 2>&1 </dev/null &
        pat="__pulsix-play-""supervisor"
        for p in /proc/[0-9]*; do
          pid=${p#/proc/}
          [ "$pid" = "$$" ] && continue
          if tr '\0' ' ' <"$p/cmdline" 2>/dev/null | grep -q -- "$pat"; then
            kill -9 "$pid"
          fi
        done
        sleep 1
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-0000000000d2"

	_, events, logs, err := runOneJobErr(t, job)
	if err == nil {
		t.Fatalf("the loop went on with a play-uid process alive after its sweeps:\n%s\n%s\n%s", eventDump(events), logs, debugPlayUIDProcesses(t))
	}
	if !strings.Contains(err.Error(), "survived") {
		t.Fatalf("Loop.Run = %v, want the surviving processes named", err)
	}
}
