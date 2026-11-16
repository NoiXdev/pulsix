package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/all"
)

// deadline is the safety net on one probe of interval: min(interval,
// maxDeadline). See maxDeadline.
func deadline(interval time.Duration) time.Duration {
	return min(interval, maxDeadline)
}

// safeExecute is execute with a panic contained: a probe reads untrusted
// network input (net/http, crypto/tls, x/net/icmp, DNS answers), and one
// hostile target must not take the agent down. A panicked round is logged
// with its stack and delivers nothing, as on the manager; the slot runs
// again on schedule.
func (r *Runner) safeExecute(ctx context.Context, s *slot) (obs []probe.Observation) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("a probe panicked; this round is dropped",
				"target_host_id", s.spec.TargetHostID, "check_key", s.spec.CheckKey,
				"plugin", s.spec.Plugin, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			obs = nil
		}
	}()
	return r.execute(ctx, s)
}

// execute runs one probe of s. Params that did not decode, or a plugin
// this agent has no probe for, are that slot's metrics as errors, without
// dialling anything.
func (r *Runner) execute(ctx context.Context, s *slot) []probe.Observation {
	plugin := s.spec.Plugin
	if s.paramsErr != nil {
		return failAll(plugin, nil, "unreadable probe params: "+s.paramsErr.Error())
	}
	f, ok := r.reg[plugin]
	if !ok {
		return failAll(plugin, s.params, "unknown probe plugin "+plugin)
	}
	pctx, cancel := context.WithTimeout(ctx, deadline(s.interval))
	defer cancel()
	return f(pctx, r.guard, s.params, s.spec.Address, nil)
}

// failAll is msg on every metric a probe of plugin with params would have
// produced: the plugin's own FailAll where it has one (all.FailAll),
// otherwise each of its descriptor's metrics, item "" — and for a plugin
// no descriptor knows either, its "<plugin>.up", as probe.Registry.Run
// reports one.
func failAll(plugin string, params map[string]any, msg string) []probe.Observation {
	if obs, ok := all.FailAll(plugin, params, msg); ok {
		return obs
	}
	if d, ok := checkplugins.Get(plugin); ok && len(d.Metrics) > 0 {
		metrics := make([]string, len(d.Metrics))
		for i, m := range d.Metrics {
			metrics[i] = m.Name
		}
		return probe.FailAll(msg, metrics...)
	}
	return probe.FailAll(msg, plugin+".up")
}

// decodeParams reads a spec's canonical params JSON; "" is no params. JSON
// that is not an object is an error.
func decodeParams(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	return params, nil
}
