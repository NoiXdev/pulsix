package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// agentTargets are the platforms the agent ships for
// (scripts/agent-binaries.sh).
var agentTargets = []string{
	"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64",
}

// TestAgentLeavesOutSNMP: agents never run the snmp probe (an snmp check
// always names a secret, and a check with a secret is probed only from the
// manager), so its library must not be linked into the agent binary on any
// platform the agent ships for (backlog 3.78). The import graph is what
// keeps it out, so the graph is what this pins. It needs the go tool and
// the module graph; where either is missing (no toolchain on PATH, offline
// without a module cache) it skips rather than fails.
func TestAgentLeavesOutSNMP(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH")
	}
	for _, target := range agentTargets {
		goos, goarch, _ := strings.Cut(target, "/")
		t.Run(target, func(t *testing.T) {
			cmd := exec.Command(gobin, "list", "-deps", "github.com/noixdev/pulsix/cmd/agent")
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
			out, err := cmd.Output()
			if err != nil {
				var stderr []byte
				if ee, ok := err.(*exec.ExitError); ok {
					stderr = ee.Stderr
				}
				t.Skipf("go list cannot resolve the module graph here: %v\n%s", err, stderr)
			}
			for _, pkg := range strings.Fields(string(out)) {
				if strings.Contains(pkg, "gosnmp") || pkg == "github.com/noixdev/pulsix/internal/probe/snmp" {
					t.Errorf("the agent links %s", pkg)
				}
			}
		})
	}
}
