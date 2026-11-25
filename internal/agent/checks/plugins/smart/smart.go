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
// disks share one path), which is named "<path> [<type>]" (devices.go).
//
// Every device that answered with readings also reports smart.power_mode
// active. A disk in standby is left asleep and reports smart.power_mode
// standby and nothing else that round (backlog 3.9b): the manager then
// stops asking for its other metrics, so their last readings neither turn
// stale nor let the disk be forgotten while it sleeps, and are judged
// again once it reports active. A device without SMART data reports no
// power mode.
//
// Item "" carries smart.devices on every collection that got as far as
// querying: the number of devices found (or configured) and not refused.
// Everything that concerns no single device is an error on that metric
// instead, so the next good round replaces it: smartctl missing, the scan
// failing or finding nothing, every device refused, and no device with
// SMART data — at least one device answered without readings (a virtual
// machine's disks, a USB stick), and every other one failed: none had
// readings and none is asleep. In that last case nothing else is reported
// either, apart from each failed device's own error. A round in which
// every device failed reports the count: the failures are on the
// devices' own items (backlog 3.55).
//
// A field a device stated and no longer states (or states with another
// type) while the device still reports is reported as "<metric> is no
// longer reported" each round, for up to 24 h (checks.VanishedMetrics,
// backlog 3.55); the plugin is a checks.StatefulPlugin for that memory. A
// sleeping disk does not report in that sense: its standby round bypasses
// the memory (Collect).
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
	// now is the vanished-metric memory's clock; nil means time.Now.
	now func() time.Time

	vanished checks.VanishedMetrics
}

// New returns the smart plugin wired to the smartctl found on the host.
func New() checks.Plugin {
	busy := newInflight()
	return &smartPlugin{smartctl: func() (runner, error) {
		l := locator{goos: runtime.GOOS, getenv: os.Getenv, lookPath: exec.LookPath, isAbs: filepath.IsAbs}
		path, err := l.find()
		if err != nil {
			return nil, err
		}
		return execRunner{path: path, busy: busy}, nil
	}, now: time.Now}
}

// NewInstance returns a plugin with the same smartctl, bounds and clock
// and an empty memory. Every instance shares the prototype's smartctl and
// with it its record of stuck runs (execRunner.busy), so a smartctl one
// check key gave up on still keeps every key from starting another on the
// same device.
func (p *smartPlugin) NewInstance() checks.Plugin {
	return &smartPlugin{smartctl: p.smartctl, deviceTimeout: p.deviceTimeout, collectTimeout: p.collectTimeout, now: p.now}
}

func (*smartPlugin) Name() string { return "smart" }

func (p *smartPlugin) timeouts() (device, collect time.Duration) {
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
//
// What was gathered goes through the instance's memory, which adds an
// error for every field a device that still reports no longer states.
// Item "" goes through it too, to no effect: its one metric,
// smart.devices, is on every round as a count or an error.
//
// A sleeping disk's round (smart.power_mode standby, backlog 3.9b) is kept
// out of the memory. Its label would otherwise count as the disk still
// reporting, and every field it stated awake would be flagged as no longer
// reported while it sleeps. Kept out, the round neither flags nor
// refreshes anything: a disk that wakes within 24 h of its last readings
// is held to the fields it stated then, and one that slept longer has
// been forgotten by the memory's own 24 h rule and starts afresh — rather
// than a stream of standby rounds keeping its memory alive indefinitely.
func (p *smartPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	obs, err := p.collect(ctx, params)
	if len(obs) == 0 {
		return obs, err // a dead context, or nothing to remember
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	awake, asleep := splitAsleep(obs)
	return append(p.vanished.Apply(now(), awake), asleep...), err
}

// splitAsleep separates the sleeping disks' observations — each such disk
// reports smart.power_mode standby and nothing else — from the rest,
// keeping the order within each.
func splitAsleep(obs []checks.Observation) (awake, asleep []checks.Observation) {
	for _, o := range obs {
		if o.Metric == powerModeMetric && o.Err == "" && o.Label != nil && *o.Label == powerModeStandby {
			asleep = append(asleep, o)
			continue
		}
		awake = append(awake, o)
	}
	return awake, asleep
}

func (p *smartPlugin) collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
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
	var someAnswer, someNoData bool // a device had data or is asleep; one answered without data
	for _, o := range outcomes {
		obs = append(obs, o.obs...)
		if o.err != nil {
			errs = append(errs, o.err)
		}
		someAnswer = someAnswer || o.answer == answerData || o.answer == answerStandby
		someNoData = someNoData || o.answer == answerNoData
	}
	if someNoData && !someAnswer {
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

var _ checks.StatefulPlugin = (*smartPlugin)(nil)
