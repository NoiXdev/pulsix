package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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
// many run. It observes only; whether "exited" is a problem is a rule's call.
type dockerPlugin struct {
	engine func(socket string) (dockerEngine, error)
	// timeout overrides dockerCollectTimeout; zero means the default.
	timeout time.Duration
}

func newDockerPlugin() Plugin {
	return dockerPlugin{engine: newDockerEngine}
}

func (dockerPlugin) Name() string { return "docker" }

// dockerParams is the parsed, defaulted configuration.
type dockerParams struct {
	labeled bool
	label   string // "key" or "key=value"
	socket  string // "" means the platform default
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
	return p
}

// optOutKey is the label key whose value "false" excludes a container in "all"
// mode: the part of the label before the first "=".
func (p dockerParams) optOutKey() string {
	key, _, _ := strings.Cut(p.label, "=")
	return key
}

// Collect lists the containers, inspects the selected ones and reports
// them. The plugin's own deadline (timeout) firing is a slow engine: what was
// gathered is reported, the rest as error observations. The caller's context
// ending is the agent shutting down: Collect then returns nothing but
// ctx.Err(), per the Plugin contract.
func (p dockerPlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
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

	engine, err := p.engine(cfg.socket)
	if err != nil {
		return dockerDown(fmt.Errorf("docker: %w", err))
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
		return dockerDown(err)
	}

	selected, errs := reportableContainers(selectContainers(list, cfg))
	details := inspectAll(ctx, engine, selected)
	if perr := parent.Err(); perr != nil {
		return nil, perr
	}

	obs := make([]Observation, 0, len(selected)*4+2)
	var total, running float64
	for i, c := range selected {
		d := details[i]
		if errors.Is(d.err, errContainerGone) {
			continue
		}
		total++
		if c.State == "running" {
			running++
		}
		if d.err != nil {
			e := inspectError(c.Name, d.err)
			for _, m := range []string{"docker.state", "docker.health", "docker.restart_count", "docker.exit_code"} {
				obs = append(obs, errObservation(c.Name, m, e))
			}
			continue
		}
		obs = append(obs,
			dockerLabel(c.Name, "docker.state", "state", c.State),
			dockerLabel(c.Name, "docker.health", "health", d.detail.Health),
			valueObservation(c.Name, "docker.restart_count", float64(d.detail.RestartCount)),
			// The engine's last exit code, whatever the state: reported
			// always, so a restarted container does not keep a stale one.
			valueObservation(c.Name, "docker.exit_code", float64(d.detail.ExitCode)),
		)
	}
	obs = append(obs,
		valueObservation("", "docker.containers_running", running),
		valueObservation("", "docker.containers_total", total),
	)
	if err := ctx.Err(); err != nil {
		// The deadline hit mid-inspect: what was gathered is still reported.
		errs = append(errs, fmt.Errorf("docker: %w", err))
	}
	return obs, errors.Join(errs...)
}

// reportableContainers drops the containers whose name the manager's
// check-results contract would refuse as an item (reportableItem): one such
// item makes the manager refuse the whole batch and end the host's stream
// (backlog 7.40), and a container name is whatever whoever can start a
// container chose. Each one left out is named, quoted, in the returned
// errors; it is not inspected and not counted.
func reportableContainers(cs []containerSummary) ([]containerSummary, []error) {
	out := make([]containerSummary, 0, len(cs))
	var errs []error
	for _, c := range cs {
		if !reportableItem(c.Name) {
			errs = append(errs, fmt.Errorf("docker: container %q cannot be reported: name longer than %d bytes, not valid UTF-8 or carrying a control character", c.Name, maxItemLen))
			continue
		}
		out = append(out, c)
	}
	return out, errs
}

// dockerLabel reports an engine-supplied state as a label, or as an error
// observation when the manager's contract would refuse it (reportableLabel).
func dockerLabel(item, metric, what, label string) Observation {
	if !reportableLabel(label) {
		return errObservation(item, metric, fmt.Errorf("docker: %s of %s cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a NUL byte", what, item, maxLabelLen))
	}
	return labelObservation(item, metric, label)
}

// inspectError names the container in a failed inspect. The client's error
// already reads "docker: inspect container: <cause>"; the container's name
// takes the place of that step instead of repeating it.
func inspectError(name string, err error) error {
	var ee *engineError
	if errors.As(err, &ee) {
		if ee.op == opInspect {
			return fmt.Errorf("docker: inspect %s: %w", name, ee.err)
		}
		return fmt.Errorf("docker: inspect %s: %s: %w", name, ee.op, ee.err)
	}
	return fmt.Errorf("docker: inspect %s: %w", name, err)
}

// dockerDown reports an unreachable engine on the two host-level metrics.
func dockerDown(err error) ([]Observation, error) {
	return []Observation{
		errObservation("", "docker.containers_running", err),
		errObservation("", "docker.containers_total", err),
	}, err
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
	sem := make(chan struct{}, dockerMaxInspects)
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res[i].detail, res[i].err = engine.Inspect(ctx, c.ID)
		}()
	}
	wg.Wait()
	return res
}

var _ Plugin = dockerPlugin{}
