// The worker's umask and the setuid helper (backlog 7.92): the helper floors
// the inherited mask at 027, so what the play uid and galaxy create stays
// readable to the worker's group whatever umask the worker was started with.
package uidboundary

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestRequirementsUnderAWorkerUmaskOf077 pins that a worker started with
// umask 077 still installs its requirements. Evidence (ansible-core 2.21.4):
// galaxy inherits the worker's umask across the setuid helper and the
// supervisor and sets none of its own, so before the fix what galaxy created
// was owner-only to the play uid; the worker, which only shares the group,
// could not open it and the take-over failed with "openat: permission
// denied". The fix is the helper's floor (TestSetuidHelperFloorsTheUmask):
// 077 becomes 027 for the play uid, and the run ends with the same files as
// under umask 022.
func TestRequirementsUnderAWorkerUmaskOf077(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)
	const repoID = "01900000-0000-7000-8000-0000000000f5"

	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	result, events, logs := runOneJob(t, repoJob(t, "01900000-0000-7000-8000-0000000000e5", repoID, gitFixture(t, installFixture(t))))

	assertPublishedInstall(t, repoID, result, events, logs)
}

// TestSetuidHelperFloorsTheUmask: the helper clears the group read and
// execute bits (027) from the worker's umask before it execs the play and
// never loosens what the worker asked for. 077 becomes 027; 027, 022, 007 and
// 002 keep their value.
func TestSetuidHelperFloorsTheUmask(t *testing.T) {
	inImage(t)
	helper := workerConfig(t).SetuidHelper

	for _, tc := range []struct {
		worker int
		want   string
	}{
		{0o077, "0027"},
		{0o027, "0027"},
		{0o022, "0022"},
		{0o007, "0007"},
		{0o002, "0002"},
	} {
		t.Run(fmt.Sprintf("%03o", tc.worker), func(t *testing.T) {
			old := syscall.Umask(tc.worker)
			t.Cleanup(func() { syscall.Umask(old) })

			out, err := exec.Command(helper, strconv.Itoa(playUID), "/bin/sh", "-c", "umask").CombinedOutput()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("the play uid's umask under a worker umask of %04o = %q, want %q", tc.worker, got, tc.want)
			}
		})
	}
}
