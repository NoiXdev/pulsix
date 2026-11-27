package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestAgentLeavesOutSNMP: agents never run the snmp probe (an snmp check
// always names a secret, and a check with a secret is probed only from the
// manager), so its library must not be linked into the agent binary on any
// platform the agent ships for (backlog 3.78). The import graph is what
// keeps it out, so the graph is what this pins.
func TestAgentLeavesOutSNMP(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	for _, goos := range []string{"linux", "windows", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			cmd := exec.Command(gobin, "list", "-deps", "github.com/noixdev/pulsix/cmd/agent")
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, out)
			}
			for _, pkg := range strings.Fields(string(out)) {
				if strings.Contains(pkg, "gosnmp") || pkg == "github.com/noixdev/pulsix/internal/probe/snmp" {
					t.Errorf("the agent links %s", pkg)
				}
			}
		})
	}
}
