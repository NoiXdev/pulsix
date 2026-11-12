//go:build darwin

package checks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

const maxStderr = 200

// launchctlLister reads `launchctl list` once per call and looks each label
// up exactly in its output.
type launchctlLister struct{}

func newUnitLister() unitLister { return launchctlLister{} }

func (launchctlLister) States(ctx context.Context, units []string) (map[string]unitResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "list")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) > maxStderr {
			msg = msg[:maxStderr]
		}
		return nil, fmt.Errorf("launchctl list: %w: %s", err, msg)
	}
	jobs, err := parseLaunchctlList(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("launchctl list: %w", err)
	}
	out := make(map[string]unitResult, len(units))
	for _, u := range units {
		job, found := jobs[u]
		out[u] = unitResult{Label: launchdLabel(job, found)}
	}
	return out, nil
}
