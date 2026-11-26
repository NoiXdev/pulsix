// Package docker is the agent's container check plugin: state, health,
// restart count and last exit code of the watched containers, read from
// the engine API over its local socket or named pipe (client.go,
// dial_*.go), and, with the resources option, their CPU, memory, network
// and block I/O usage (stats.go, resources.go). Rates need the previous
// reading, so it is a checks.StatefulPlugin like network. The Plugin
// contract and the shared observation helpers it builds on are in
// internal/agent/checks.
package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

const (
	dockerDefaultSelection = "all"
	dockerDefaultLabel     = "pulsix.monitor=true"

	// dockerMaxInspects bounds the inspects in flight at once.
	dockerMaxInspects = 8

	// dockerCollectTimeout bounds one Collect. The scheduler passes no
	// per-collect deadline and the minimum check interval is 10 s, so a hung
	// engine must not hold a tick longer than that.
	dockerCollectTimeout = 10 * time.Second
)

// dockerPlugin reports each watched container's state, health, restart count
// and last exit code, plus how many containers are watched and how
// many run, and with the resources option their usage. It observes only;
// whether "exited" is a problem is a rule's call. Every check key collects
// with its own instance (NewInstance), whose baselines are its own previous
// round; an instance is collected by its key's one goroutine only, so it
// holds no lock.
type dockerPlugin struct {
	engine func(socket string) (dockerEngine, error)
	// timeout overrides dockerCollectTimeout; zero means the default.
	timeout time.Duration

	// prev is each sampled container's previous reading, by container ID
	// (resources.go); nil while the resources option is off.
	prev map[string]statsSample
	// reported is, by container ID, the usage metrics the container's last
	// running reading had: what it reports as zero while stopped
	// (resources.go); nil while the resources option is off.
	reported map[string][]string
}

// New returns the docker plugin wired to the engine API client. It is a
// checks.StatefulPlugin prototype: the scheduler collects only the
// instances NewInstance hands out, one per check key.
func New() checks.Plugin {
	return &dockerPlugin{engine: newDockerEngine}
}

// NewInstance returns a plugin with the same engine and no baselines.
func (p *dockerPlugin) NewInstance() checks.Plugin {
	return &dockerPlugin{engine: p.engine, timeout: p.timeout}
}

func (*dockerPlugin) Name() string { return "docker" }

// dockerParams is the parsed, defaulted configuration.
type dockerParams struct {
	labeled bool
	label   string // "key" or "key=value"
	socket  string // "" means the platform default
	// resources asks for each container's usage (backlog 3.12a); off by
	// default.
	resources bool
}

// parseDockerParams reads the params defensively: a wire round trip can change
// types, and the manager has already validated them, so a value of the wrong
// type falls back to its default instead of failing, and an unknown selection
// is treated as "all".
func parseDockerParams(params map[string]any) dockerParams {
	str := func(key string) string {
		s, _ := params[key].(string)
		return s
	}
	p := dockerParams{label: str("label"), socket: str("socket")}
	if p.label == "" {
		p.label = dockerDefaultLabel
	}
	p.labeled = str("selection") == "labeled"
	p.resources, _ = params["resources"].(bool)
	return p
}

// optOutKey is the label key whose value "false" excludes a container in "all"
// mode: the part of the label before the first "=".
func (p dockerParams) optOutKey() string {
	key, _, _ := strings.Cut(p.label, "=")
	return key
}

// Collect lists the containers, inspects the selected ones and reports
// them; with the resources option it then reads their usage with what is
// left of the deadline (sampleResources), so a slow stats endpoint never
// costs the state metrics. The plugin's own deadline (timeout) firing is a
// slow engine: what was gathered is reported, the rest as error
// observations. The caller's context ending is the agent shutting down:
// Collect then returns nothing but ctx.Err(), per the Plugin contract.
func (p *dockerPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = dockerCollectTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cfg := parseDockerParams(params)
	if !cfg.resources {
		p.prev, p.reported = nil, nil
	}

	engine, err := p.engine(cfg.socket)
	if err != nil {
		return dockerDown(fmt.Errorf("docker: %w", err), cfg.resources)
	}
	filter := ""
	if cfg.labeled {
		filter = cfg.label
	}
	list, err := engine.List(ctx, filter)
	if err != nil {
		if perr := parent.Err(); perr != nil {
			return nil, perr
		}
		if derr := ctx.Err(); derr != nil && errors.Is(err, derr) {
			err = fmt.Errorf("docker: engine did not answer in time: %w", err)
		}
		return dockerDown(err, cfg.resources)
	}

	selected, errs := reportableContainers(selectContainers(list, cfg))
	unreported := float64(len(errs)) // refused names only; a later deadline error is no container
	details := inspectAll(ctx, engine, selected)
	if perr := parent.Err(); perr != nil {
		return nil, perr
	}

	obs := make([]checks.Observation, 0, len(selected)*4+4)
	present := make([]containerSummary, 0, len(selected))
	started := make(map[string]string, len(selected))
	var total, running float64
	for i, c := range selected {
		d := details[i]
		if errors.Is(d.err, errContainerGone) {
			continue
		}
		present = append(present, c)
		started[c.ID] = d.detail.StartedAt
		total++
		if c.State == "running" {
			running++
		}
		if d.err != nil {
			e := inspectError(c.Name, d.err)
			for _, m := range []string{"docker.state", "docker.health", "docker.restart_count", "docker.exit_code"} {
				obs = append(obs, checks.ErrObservation(c.Name, m, e))
			}
			continue
		}
		obs = append(obs,
			dockerLabel(c.Name, "docker.state", "state", c.State),
			dockerLabel(c.Name, "docker.health", "health", d.detail.Health),
			checks.ValueObservation(c.Name, "docker.restart_count", float64(d.detail.RestartCount)),
			// The engine's last exit code, whatever the state: reported
			// always, so a restarted container does not keep a stale one.
			checks.ValueObservation(c.Name, "docker.exit_code", float64(d.detail.ExitCode)),
		)
	}
	obs = append(obs,
		checks.ValueObservation("", "docker.containers_running", running),
		checks.ValueObservation("", "docker.containers_total", total),
		checks.ValueObservation("", "docker.containers_unreported", unreported),
	)
	if cfg.resources {
		usage, unsampled, uerr := p.sampleResources(ctx, engine, present, started)
		if perr := parent.Err(); perr != nil {
			return nil, perr
		}
		obs = append(obs, usage...)
		obs = append(obs, checks.ValueObservation("", "docker.containers_unsampled", float64(unsampled)))
		if uerr != nil {
			errs = append(errs, uerr)
		}
	}
	if err := ctx.Err(); err != nil {
		// The deadline hit mid-inspect or mid-stats: what was gathered is
		// still reported.
		errs = append(errs, fmt.Errorf("docker: %w", err))
	}
	return obs, errors.Join(errs...)
}

// reportableContainers drops the containers whose name the manager's
// check-results contract would refuse as an item (ReportableItem): one such
// item makes the manager refuse the whole batch and end the host's stream
// (backlog 7.40), and a container name is whatever whoever can start a
// container chose. Each one left out is named, quoted, in the returned
// errors; it is not inspected and not counted.
func reportableContainers(cs []containerSummary) ([]containerSummary, []error) {
	out := make([]containerSummary, 0, len(cs))
	var errs []error
	for _, c := range cs {
		if !checks.ReportableItem(c.Name) {
			errs = append(errs, fmt.Errorf("docker: container %q cannot be reported: name longer than %d bytes, not valid UTF-8 or carrying a control character", c.Name, checks.MaxItemLen))
			continue
		}
		out = append(out, c)
	}
	return out, errs
}

// dockerLabel reports an engine-supplied state as a label, or as an error
// observation when the manager's contract would refuse it (ReportableLabel).
func dockerLabel(item, metric, what, label string) checks.Observation {
	if !checks.ReportableLabel(label) {
		return checks.ErrObservation(item, metric, fmt.Errorf("docker: %s of %s cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a NUL byte", what, item, checks.MaxLabelLen))
	}
	return checks.LabelObservation(item, metric, label)
}

// inspectError names the container in a failed inspect. The client's error
// already reads "docker: inspect container: <cause>"; the container's name
// takes the place of that step instead of repeating it.
func inspectError(name string, err error) error {
	return containerError("inspect", opInspect, name, err)
}

// containerError is "docker: <verb> <name>: <cause>" for a failed request
// op about one container, the client's own step left out.
func containerError(verb, op, name string, err error) error {
	var ee *engineError
	if errors.As(err, &ee) {
		if ee.op == op {
			return fmt.Errorf("docker: %s %s: %w", verb, name, ee.err)
		}
		return fmt.Errorf("docker: %s %s: %s: %w", verb, name, ee.op, ee.err)
	}
	return fmt.Errorf("docker: %s %s: %w", verb, name, err)
}

// dockerDown reports an unreachable engine on the host-level metrics: the
// three counts, and the unsampled count while resources are on.
func dockerDown(err error, resources bool) ([]checks.Observation, error) {
	obs := []checks.Observation{
		checks.ErrObservation("", "docker.containers_running", err),
		checks.ErrObservation("", "docker.containers_total", err),
		checks.ErrObservation("", "docker.containers_unreported", err),
	}
	if resources {
		obs = append(obs, checks.ErrObservation("", "docker.containers_unsampled", err))
	}
	return obs, err
}

// selectContainers drops repeated IDs (first wins) and, in "all" mode, the
// containers that opted out. In "labeled" mode the engine already filtered.
func selectContainers(list []containerSummary, cfg dockerParams) []containerSummary {
	seen := make(map[string]struct{}, len(list))
	key := cfg.optOutKey()
	out := make([]containerSummary, 0, len(list))
	for _, c := range list {
		if _, dup := seen[c.ID]; dup {
			continue
		}
		seen[c.ID] = struct{}{}
		if !cfg.labeled {
			if v, ok := c.Labels[key]; ok && strings.ToLower(strings.TrimSpace(v)) == "false" {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

type inspectResult struct {
	detail containerDetail
	err    error
}

// inspectAll inspects every container with at most dockerMaxInspects in
// flight; results are index-aligned with cs.
func inspectAll(ctx context.Context, engine dockerEngine, cs []containerSummary) []inspectResult {
	res := make([]inspectResult, len(cs))
	forEachLimited(len(cs), dockerMaxInspects, func(i int) {
		res[i].detail, res[i].err = engine.Inspect(ctx, cs[i].ID)
	})
	return res
}

// forEachLimited calls fn(i) for every i below n, at most limit at once,
// and returns once every call has.
func forEachLimited(n, limit int, fn func(i int)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

var (
	_ checks.Plugin         = (*dockerPlugin)(nil)
	_ checks.StatefulPlugin = (*dockerPlugin)(nil)
)
