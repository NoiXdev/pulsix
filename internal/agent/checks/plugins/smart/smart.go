// Package smart is the agent's disk health check plugin: it runs
// smartmontools' smartctl (as the agent's user, root or SYSTEM) and reports
// each disk's SMART health, temperature, power-on hours, the ATA sector
// counters and the NVMe wear and error counters (report.go lists the JSON
// fields). The Plugin contract and the shared observation helpers it
// builds on are in internal/agent/checks.
//
// smartctl is executed directly with a fixed argument list (runner.go),
// never through a shell: `smartctl --scan --json` lists the devices unless
// the devices parameter names them, then `smartctl --json -a -n standby,0
// [-d <type>] <device>` reads each one. One item per device, named by the
// path as configured or scanned — except a disk behind a RAID controller
// (a megaraid, cciss, 3ware, areca, aacraid, sssraid or hpt -d type, where
// disks share one path), which is named "<path> [<type>]" (devices.go). A
// disk in standby is left asleep and reports nothing that round.
//
// Item "" carries smart.devices on every collection that got as far as
// querying: the number of devices found (or configured) and not refused.
// Everything that concerns no single device is an error on that metric
// instead, so the next good round replaces it: smartctl missing, the scan
// failing or finding nothing, every device refused, and no device with
// SMART data — every device answered without readings (a virtual
// machine's disks, a USB stick) and none of them is asleep. In that last
// case nothing else is reported either, apart from each failed device's
// own error.
//
// Bounds: at most maxParallel devices are queried at once, each within
// deviceTimeout, the whole collection within collectTimeout; a device
// still running or not yet started when that ends gets an error. Output
// is capped at 1 MiB per run.
package smart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

const (
	maxParallel           = 4
	defaultDeviceTimeout  = 20 * time.Second
	defaultCollectTimeout = 60 * time.Second
)

// smartPlugin reports SMART readings per device.
type smartPlugin struct {
	// smartctl finds the binary and returns a runner for it, or
	// errNotFound. It is asked on every Collect, so installing
	// smartmontools takes effect without restarting the agent.
	smartctl func() (runner, error)
	// deviceTimeout and collectTimeout override the defaults; zero means
	// the default.
	deviceTimeout  time.Duration
	collectTimeout time.Duration
}

// New returns the smart plugin wired to the smartctl found on the host.
func New() checks.Plugin {
	busy := newInflight()
	return smartPlugin{smartctl: func() (runner, error) {
		l := locator{goos: runtime.GOOS, getenv: os.Getenv, lookPath: exec.LookPath, isAbs: filepath.IsAbs}
		path, err := l.find()
		if err != nil {
			return nil, err
		}
		return execRunner{path: path, busy: busy}, nil
	}}
}

func (smartPlugin) Name() string { return "smart" }

func (p smartPlugin) timeouts() (device, collect time.Duration) {
	device, collect = p.deviceTimeout, p.collectTimeout
	if device <= 0 {
		device = defaultDeviceTimeout
	}
	if collect <= 0 {
		collect = defaultCollectTimeout
	}
	return device, collect
}

// Collect queries every device and reports what each states. The plugin's
// own deadline firing is a slow host: what was gathered is reported, the
// rest as error observations. The caller's context ending is the agent
// shutting down: Collect then returns nothing but ctx.Err(), per the Plugin
// contract.
func (p smartPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.smartctl == nil {
		return nil, nil
	}
	paths, _, err := checks.ParamStrings(params, "devices")
	if err != nil {
		return nil, fmt.Errorf("smart: %w", err)
	}
	deviceTimeout, collectTimeout := p.timeouts()
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, collectTimeout)
	defer cancel()

	run, err := p.smartctl()
	if err != nil {
		return hostError(err)
	}

	var devs []device
	var errs []error
	if len(paths) > 0 {
		devs, errs = configuredDevices(paths)
	} else {
		devs, errs, err = scan(ctx, run, deviceTimeout, collectTimeout)
		if perr := parent.Err(); perr != nil {
			return nil, perr
		}
		if err != nil {
			return hostError(errors.Join(append([]error{err}, errs...)...))
		}
		if len(devs) == 0 && len(errs) == 0 {
			return hostError(errors.New("smart: smartctl --scan found no devices"))
		}
	}
	if len(devs) == 0 {
		// Every device was refused: say why where the check shows it.
		return hostError(errors.Join(errs...))
	}

	outcomes := queryAll(ctx, run, devs, deviceTimeout, collectTimeout)
	if perr := parent.Err(); perr != nil {
		return nil, perr
	}
	var obs []checks.Observation
	var someAnswer bool // a device had data or is asleep
	for _, o := range outcomes {
		obs = append(obs, o.obs...)
		if o.err != nil {
			errs = append(errs, o.err)
		}
		someAnswer = someAnswer || o.answer == answerData || o.answer == answerStandby
	}
	if !someAnswer {
		err := errors.New("smart: no device with SMART data")
		errs = append(errs, err)
		obs = append(obs, checks.ErrObservation("", devicesMetric, err))
		return obs, errors.Join(errs...)
	}
	obs = append(obs, checks.ValueObservation("", devicesMetric, float64(len(devs))))
	return obs, errors.Join(errs...)
}

// devicesMetric is the host-level count on item "" that also carries every
// failure concerning no single device.
const devicesMetric = "smart.devices"

// hostError reports a failure that concerns no single device on item "".
func hostError(err error) ([]checks.Observation, error) {
	return []checks.Observation{checks.ErrObservation("", devicesMetric, err)}, err
}

// scan lists the devices with `smartctl --scan --json`.
func scan(ctx context.Context, run runner, deviceTimeout, collectTimeout time.Duration) ([]device, []error, error) {
	sctx, cancel := context.WithTimeout(ctx, deviceTimeout)
	defer cancel()
	res, err := run.Run(sctx, scanArgs)
	if err != nil {
		return nil, nil, runError("smart: smartctl --scan", err, ctx, sctx, deviceTimeout, collectTimeout)
	}
	return parseScan(res)
}

type outcome struct {
	obs    []checks.Observation
	answer answer
	err    error
}

// queryAll queries devs with at most maxParallel runs at once and returns
// one outcome per device, in order. It returns when every device has
// answered or ctx (the collection's bound) ends, whichever is first —
// even if a runner does not return when told to; a device without an
// answer by then gets an error observation.
func queryAll(ctx context.Context, run runner, devs []device, deviceTimeout, collectTimeout time.Duration) []outcome {
	type indexed struct {
		i int
		outcome
	}
	results := make(chan indexed, len(devs)) // never blocks a late worker
	go func() {
		sem := make(chan struct{}, maxParallel)
		for i, d := range devs {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				defer func() { <-sem }()
				obs, ans, err := query(ctx, run, d, deviceTimeout, collectTimeout)
				results <- indexed{i, outcome{obs, ans, err}}
			}()
		}
	}()

	out := make([]outcome, len(devs))
	answered := make([]bool, len(devs))
	take := func(r indexed) {
		out[r.i], answered[r.i] = r.outcome, true
	}
wait:
	for n := 0; n < len(devs); n++ {
		select {
		case r := <-results:
			take(r)
		case <-ctx.Done():
			for {
				select {
				case r := <-results:
					take(r)
				default:
					break wait
				}
			}
		}
	}
	for i, d := range devs {
		if !answered[i] {
			err := fmt.Errorf("smart: %s: no answer within the check's %s time limit", d.item, collectTimeout)
			out[i] = outcome{[]checks.Observation{checks.ErrObservation(d.item, "smart.passed", err)}, answerFailed, err}
		}
	}
	return out
}

// query runs smartctl for one device within deviceTimeout.
func query(ctx context.Context, run runner, d device, deviceTimeout, collectTimeout time.Duration) ([]checks.Observation, answer, error) {
	dctx, cancel := context.WithTimeout(ctx, deviceTimeout)
	defer cancel()
	res, err := run.Run(dctx, d.args())
	if err != nil {
		err = runError("smart: "+d.item, err, ctx, dctx, deviceTimeout, collectTimeout)
		return []checks.Observation{checks.ErrObservation(d.item, "smart.passed", err)}, answerFailed, err
	}
	return interpret(d, res)
}

// runError words a failed run: which of the two deadlines stopped it, or
// the runner's own error.
func runError(what string, err error, collect, run context.Context, deviceTimeout, collectTimeout time.Duration) error {
	switch {
	case collect.Err() != nil:
		return fmt.Errorf("%s: no answer within the check's %s time limit", what, collectTimeout)
	case run.Err() != nil:
		return fmt.Errorf("%s: smartctl did not answer within %s", what, deviceTimeout)
	}
	return fmt.Errorf("%s: %w", what, err)
}

var _ checks.Plugin = smartPlugin{}
