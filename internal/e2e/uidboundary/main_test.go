package uidboundary

import (
	"os"
	"testing"

	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// TestMain makes this test binary its own play supervisor. The tests here
// drive the real worker through worker.Load, which on Linux names
// os.Executable() — this binary, inside the image — as Config.PlaySupervisor,
// so the runner re-executes it with the supervisor's reserved argument for
// every run. Without this dispatch that re-exec would run the test suite
// instead of the play, print its PASS line onto the event stream and exit 0
// with no run stats. cmd/worker and cmd/manager dispatch the same way, for
// the sweep too.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == supervisor.Arg {
		os.Exit(supervisor.Main(os.Args[2:]))
	}
	// The loop also re-executes it, as the play uid, to sweep that uid's
	// processes between jobs (backlog 7.21); without this the sweep would
	// run the whole suite under the play uid.
	if len(os.Args) > 1 && os.Args[1] == supervisor.SweepArg {
		// A leftover play process can kill the sweep before it acts
		// (backlog 7.35); sweepDisabledMarker makes it do nothing, the
		// deterministic stand-in for that race.
		if _, err := os.Stat(sweepDisabledMarker); err == nil {
			os.Exit(0)
		}
		os.Exit(supervisor.Sweep(os.Stderr))
	}
	os.Exit(m.Run())
}
