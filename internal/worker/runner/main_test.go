package runner

import (
	"os"
	"testing"
)

// TestMain removes what the package's tests built for themselves — the
// play supervisor binary of the Linux tests — once they have all run, so
// it does not pile up in the temp dir (backlog 2.114).
func TestMain(m *testing.M) {
	code := m.Run()
	cleanupSupervisorBuild()
	os.Exit(code)
}
