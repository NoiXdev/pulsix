// The play uid's sweep between jobs, proven in the image (backlog 7.21).
package uidboundary

import (
	"strings"
	"testing"
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
