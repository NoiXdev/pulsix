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
// and (once stopped) exit code, plus how many containers are watched and how
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

func (p dockerPlugin) Collect(ctx context.Context, params map[string]any) ([]Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = dockerCollectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
		if perr := ctx.Err(); perr != nil && errors.Is(err, perr) {
			err = fmt.Errorf("docker: engine did not answer in time: %w", err)
		}
		return dockerDown(err)
	}

	selected := selectContainers(list, cfg)
	details := inspectAll(ctx, engine, selected)

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
			e := fmt.Errorf("docker: inspect %s: %w", c.Name, d.err)
			for _, m := range []string{"docker.state", "docker.health", "docker.restart_count", "docker.exit_code"} {
				obs = append(obs, errObservation(c.Name, m, e))
			}
			continue
		}
		obs = append(obs,
			labelObservation(c.Name, "docker.state", c.State),
			labelObservation(c.Name, "docker.health", d.detail.Health),
			valueObservation(c.Name, "docker.restart_count", float64(d.detail.RestartCount)),
		)
		if c.State == "exited" || c.State == "dead" {
			obs = append(obs, valueObservation(c.Name, "docker.exit_code", float64(d.detail.ExitCode)))
		}
	}
	obs = append(obs,
		valueObservation("", "docker.containers_running", running),
		valueObservation("", "docker.containers_total", total),
	)
	if err := ctx.Err(); err != nil {
		// The deadline hit mid-inspect: what was gathered is still reported.
		return obs, fmt.Errorf("docker: %w", err)
	}
	return obs, nil
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
