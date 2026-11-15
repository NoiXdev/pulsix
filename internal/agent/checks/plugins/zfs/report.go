package zfs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// statusDerived are the metrics read from zpool status (and zfs get).
var statusDerived = []string{
	"zfs.errors_read", "zfs.errors_write", "zfs.errors_checksum", "zfs.data_errors",
	"zfs.devices_unhealthy", "zfs.scan", "zfs.scan_pct", "zfs.scrub_age_days",
}

// caller makes one collection's calls, each within callTimeout and all
// within ctx, the collection's bound.
type caller struct {
	ctx            context.Context
	run            runner
	callTimeout    time.Duration
	collectTimeout time.Duration
}

// call runs one program and returns its standard output, or an error
// worded for the check: which bound stopped it, or the run's own error
// with stderr's first line — then with whatever standard output the run
// still produced (zfs get exits non-zero when one of its datasets is gone
// but prints the others). It returns when a bound ends even if the runner
// does not.
func (c caller) call(name string, args []string, what string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.callTimeout)
	defer cancel()
	type result struct {
		stdout, stderr []byte
		err            error
	}
	done := make(chan result, 1) // never blocks a late runner
	go func() {
		stdout, stderr, err := c.run.Run(ctx, name, args)
		done <- result{stdout, stderr, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		r.err = ctx.Err()
	}
	if r.err == nil {
		return r.stdout, nil
	}
	switch {
	case c.ctx.Err() != nil:
		return nil, fmt.Errorf("zfs: %s: no answer within the check's %s time limit", what, c.collectTimeout)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("zfs: %s did not answer within %s", what, c.callTimeout)
	}
	if line := cleanStderr(r.stderr); line != "" {
		return r.stdout, fmt.Errorf("zfs: %s: %w: %s", what, r.err, line)
	}
	return r.stdout, fmt.Errorf("zfs: %s: %w", what, r.err)
}

// report gathers one collection's observations and its advisory errors,
// each text once.
type report struct {
	obs  []checks.Observation
	errs []error
	seen map[string]bool
}

func newReport(errs []error) *report {
	r := &report{seen: map[string]bool{}}
	for _, err := range errs {
		r.note(err)
	}
	return r
}

func (r *report) note(err error) {
	if !r.seen[err.Error()] {
		r.seen[err.Error()] = true
		r.errs = append(r.errs, err)
	}
}

func (r *report) err() error { return errors.Join(r.errs...) }

// fail reports err, worded already, on the pool's metric.
func (r *report) fail(item, metric string, err error) {
	r.obs = append(r.obs, checks.ErrObservation(item, metric, err))
	r.note(err)
}

// number reports a parsed reading: its value, nothing when absent, or its
// error named after the pool.
func (r *report) number(item, metric string, n number) {
	switch {
	case n.absent:
	case n.err != nil:
		r.fail(item, metric, fmt.Errorf("zfs: pool %s: %w", item, n.err))
	default:
		r.obs = append(r.obs, checks.ValueObservation(item, metric, n.value))
	}
}

func (r *report) label(item, metric, label string) {
	if !checks.ReportableLabel(label) {
		r.fail(item, metric, fmt.Errorf("zfs: pool %s: %s %q cannot be reported", item, metric, cutText(label, maxQuote)))
		return
	}
	r.obs = append(r.obs, checks.LabelObservation(item, metric, label))
}

// pool reports every metric of one pool.
func (r *report) pool(lp listedPool, statuses map[string]poolStatus, statusErr error, age scrubAge) {
	name := lp.name
	r.label(name, "zfs.state", lp.health)
	r.number(name, "zfs.capacity_pct", lp.capacity)
	r.number(name, "zfs.size_bytes", lp.size)
	r.number(name, "zfs.free_bytes", lp.free)
	r.number(name, "zfs.fragmentation_pct", lp.fragmentation)

	st, ok := statuses[name]
	if statusErr == nil && !ok {
		statusErr = fmt.Errorf("zfs: pool %s: missing from zpool status", name)
	}
	if statusErr != nil {
		for _, m := range statusDerived {
			r.fail(name, m, statusErr)
		}
		return
	}
	r.number(name, "zfs.errors_read", st.readErrors)
	r.number(name, "zfs.errors_write", st.writeErrors)
	r.number(name, "zfs.errors_checksum", st.checksumErrors)
	r.number(name, "zfs.data_errors", st.dataErrors)
	r.number(name, "zfs.devices_unhealthy", st.devicesUnhealthy)
	if st.scan.kindErr != nil {
		r.fail(name, "zfs.scan", fmt.Errorf("zfs: pool %s: %w", name, st.scan.kindErr))
	} else {
		r.label(name, "zfs.scan", st.scan.kind)
	}
	r.number(name, "zfs.scan_pct", st.scan.pct)
	r.scrubAge(name, age)
}
