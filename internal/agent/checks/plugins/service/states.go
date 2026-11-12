package service

import (
	"bytes"
	"errors"
	"strings"
)

// Win32 service states (SERVICE_STATUS.dwCurrentState), local so this
// portable file never imports the Windows-only package.
const (
	winStopped         uint32 = 1
	winStartPending    uint32 = 2
	winStopPending     uint32 = 3
	winRunning         uint32 = 4
	winContinuePending uint32 = 5
	winPausePending    uint32 = 6
	winPaused          uint32 = 7
)

// winNeverStarted is ERROR_SERVICE_NEVER_STARTED, the exit code of a
// service that has not run since boot: stopped, not failed.
const winNeverStarted uint32 = 1077

// windowsLabel maps a service's current state and Win32 exit code to the
// plugin's label set. Windows has no failed state; a stopped service whose
// last exit code is an error is what an operator means by failed.
func windowsLabel(state, win32ExitCode uint32) string {
	switch state {
	case winRunning:
		return "active"
	case winStartPending, winContinuePending:
		return "activating"
	case winStopped:
		if win32ExitCode == 0 || win32ExitCode == winNeverStarted {
			return "inactive"
		}
		return "failed"
	default: // stop pending, pause pending, paused, anything unrecognized
		return "unknown"
	}
}

// launchdJob is one line of `launchctl list`: PID is "-" when the job is
// not running, Status is the last exit status.
type launchdJob struct {
	PID    string
	Status string
}

const launchctlHeader = "PID\tStatus\tLabel"

// parseLaunchctlList reads `launchctl list` output. The first non-empty
// line must be the header; later lines that do not split into exactly three
// tab-separated fields are skipped.
func parseLaunchctlList(out []byte) (map[string]launchdJob, error) {
	jobs := make(map[string]launchdJob)
	sawHeader := false
	for _, raw := range bytes.Split(out, []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !sawHeader {
			if line != launchctlHeader {
				return nil, errors.New("unexpected launchctl list output: missing PID/Status/Label header")
			}
			sawHeader = true
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		jobs[f[2]] = launchdJob{PID: f[0], Status: f[1]}
	}
	if !sawHeader {
		return nil, errors.New("empty launchctl list output")
	}
	return jobs, nil
}

// launchdLabel maps a launchd job to the plugin's label set: running is
// active, loaded and exited cleanly is inactive, loaded with a non-zero or
// signal status is failed, not loaded is unknown.
func launchdLabel(job launchdJob, found bool) string {
	switch {
	case !found:
		return "unknown"
	case job.PID != "-":
		return "active"
	case job.Status == "0":
		return "inactive"
	default:
		return "failed"
	}
}

// systemdLabel maps a raw systemd unit state to the plugin's fixed label
// set. A not-found unit (what a made-up unit name reports) and any
// ActiveState outside the four systemd states this plugin knows both
// collapse to "unknown", never an error: an unrecognized state is itself
// the observation.
func systemdLabel(loadState, activeState string) string {
	if loadState == "not-found" {
		return "unknown"
	}
	switch activeState {
	case "active", "inactive", "failed", "activating":
		return activeState
	default:
		return "unknown"
	}
}
