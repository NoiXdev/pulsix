//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
)

// serviceName is the name the installer registers with the service control
// manager.
const serviceName = "PulsixAgent"

// serviceStopWait is how long a stop request waits for the agent loop to
// return before the service gives up and reports failure.
const serviceStopWait = 20 * time.Second

// runPlatform runs under the service control manager when started as a
// service, and in the foreground when started from a console.
func runPlatform(args, env []string) int {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "detect service session:", err)
		return 1
	}
	if !isSvc {
		return run(args, env)
	}
	h := &handler{args: args, env: env}
	if err := svc.Run(serviceName, h); err != nil {
		fmt.Fprintln(os.Stderr, "run service:", err)
		return 1
	}
	return int(h.code)
}

// handler adapts serve to the service control manager.
type handler struct {
	args, env []string
	code      uint32
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	stop := make(chan struct{})
	var current svc.Status
	report := func(s svcState) {
		switch s {
		case svcStartPending:
			current = svc.Status{State: svc.StartPending}
		case svcRunning:
			current = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
		case svcStopPending:
			current = svc.Status{State: svc.StopPending}
		case svcStopped:
			current = svc.Status{State: svc.Stopped}
		}
		status <- current
	}

	// Forward control requests until serve returns. Interrogate replies with
	// the last reported status.
	finished := make(chan struct{})
	go func() {
		stopped := false
		for {
			select {
			case <-finished:
				return
			case r := <-requests:
				switch r.Cmd {
				case svc.Interrogate:
					status <- r.CurrentStatus
				case svc.Stop, svc.Shutdown:
					if !stopped {
						stopped = true
						close(stop)
					}
				}
			}
		}
	}()

	h.code = serve(stop, report, func(ctx context.Context) int {
		return runWithContext(ctx, h.args, h.env)
	}, serviceStopWait)
	close(finished)
	return false, h.code
}
