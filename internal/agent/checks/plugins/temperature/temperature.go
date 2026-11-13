// Package temperature is the agent's temperature check plugin: one item per
// sensor the operating system exposes (Linux hwmon and thermal zones, macOS
// SMC, Windows ACPI thermal zones, read through gopsutil), with temp.celsius
// and, only where the sensor states them, temp.high_celsius and
// temp.critical_celsius. The Plugin contract and the shared observation
// helpers it builds on are in internal/agent/checks.
//
// Sensor keys are not unique: two sockets both report coretemp_core_0. The
// plugin keeps every sensor by disambiguating repeated keys in the order the
// provider returns them — the first keeps its key, later ones get "#2", "#3"
// and so on appended (a name already taken, generated or real, moves on to
// the next free number). The "sensors" filter matches these final
// names. The order is the platform's enumeration order, so a hardware change
// that reorders sensors can move readings between the "#n" items.
//
// Item "" carries temp.sensors: the number of sensors reported this round,
// after the filter, on every run that reported at least one. A run that
// reported none puts "no temperature sensors found" on that metric
// instead, so the next good run replaces the error with the count.
//
// A limit a sensor stated and no longer states while the sensor still
// reads is reported as "<metric> is no longer reported" each round, for
// up to 24 h (checks.VanishedMetrics, backlog 3.55); the plugin is a
// checks.StatefulPlugin for that memory.
package temperature

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// sensorReader is the sensors.Temperatures probe. Like gopsutil it may
// return readings together with an error (warnings about sensors it could
// not read); the readings are authoritative.
type sensorReader interface {
	Temperatures(ctx context.Context) ([]sensors.TemperatureStat, error)
}

type gopsutilSensors struct{}

func (gopsutilSensors) Temperatures(ctx context.Context) ([]sensors.TemperatureStat, error) {
	return sensors.TemperaturesWithContext(ctx)
}

// temperaturePlugin reports per-sensor temperatures (item = sensor key).
// It is a checks.StatefulPlugin: a sensor may state its limits one round
// and not the next, so every check key collects with its own instance
// (NewInstance), which remembers the limits each sensor stated
// (checks.VanishedMetrics) and reports one it no longer states as an
// error instead of leaving its last value standing (backlog 3.55). An
// instance is collected by its key's one goroutine only, so it holds no
// lock.
type temperaturePlugin struct {
	Sensors sensorReader
	// now is the vanished-metric memory's clock; nil means time.Now.
	now func() time.Time

	vanished checks.VanishedMetrics
}

// New returns the temperature plugin wired to gopsutil. It is a
// checks.StatefulPlugin prototype: the scheduler collects only the
// instances NewInstance hands out, one per check key.
func New() checks.Plugin {
	return &temperaturePlugin{Sensors: gopsutilSensors{}, now: time.Now}
}

// NewInstance returns a plugin with the same provider and clock and an
// empty memory.
func (p *temperaturePlugin) NewInstance() checks.Plugin {
	return &temperaturePlugin{Sensors: p.Sensors, now: p.now}
}

func (*temperaturePlugin) Name() string { return "temperature" }

const noSensors = "no temperature sensors found"

// Collect reads the sensors (collect) and adds, through the instance's
// memory, an error for every limit a sensor that still reads no longer
// states. Item "" goes through the memory too, to no effect: its one
// metric, temp.sensors, is on every round as a count or an error.
func (p *temperaturePlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	obs, err := p.collect(ctx, params)
	if len(obs) == 0 {
		return obs, err // a dead context, or nothing to remember
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	return p.vanished.Apply(now(), obs), err
}

func (p *temperaturePlugin) collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Sensors == nil {
		return nil, nil
	}
	filter, _, err := checks.ParamStrings(params, "sensors")
	if err != nil {
		return nil, fmt.Errorf("temperature: %w", err)
	}
	var want map[string]bool
	if len(filter) > 0 {
		want = make(map[string]bool, len(filter))
		for _, s := range filter {
			want[s] = true
		}
	}

	stats, readErr := p.Sensors.Temperatures(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var errs []error
	if readErr != nil {
		errs = append(errs, fmt.Errorf("temperature: %w", readErr))
	}

	var obs []checks.Observation
	n := 0
	for i, st := range disambiguate(stats) {
		if st.name == "" {
			errs = append(errs, fmt.Errorf("temperature: sensor %d has no key and cannot be reported", i))
			continue
		}
		if want != nil && !want[st.name] {
			continue
		}
		if !checks.ReportableItem(st.name) {
			errs = append(errs, fmt.Errorf("temperature: sensor %q cannot be reported: longer than %d bytes, not valid UTF-8 or carrying a control character", st.name, checks.MaxItemLen))
			continue
		}
		if !finite(st.Temperature) {
			errs = append(errs, fmt.Errorf("temperature: sensor %q has no finite reading", st.name))
			continue
		}
		n++
		obs = append(obs, checks.ValueObservation(st.name, "temp.celsius", st.Temperature))
		for _, lim := range []struct {
			metric string
			v      float64
		}{{"temp.high_celsius", st.High}, {"temp.critical_celsius", st.Critical}} {
			switch {
			case lim.v == 0:
			case !finite(lim.v):
				errs = append(errs, fmt.Errorf("temperature: sensor %q: %s is not finite", st.name, lim.metric))
			default:
				obs = append(obs, checks.ValueObservation(st.name, lim.metric, lim.v))
			}
		}
	}

	if n == 0 {
		errs = append(errs, errors.New(noSensors))
		err := errors.Join(errs...)
		return []checks.Observation{checks.ErrObservation("", "temp.sensors", err)}, err
	}
	obs = append(obs, checks.ValueObservation("", "temp.sensors", float64(n)))
	return obs, errors.Join(errs...)
}

type namedStat struct {
	sensors.TemperatureStat
	name string
}

// disambiguate names every sensor: the first of a repeated key keeps it,
// later ones get "#2", "#3", … (see the package doc).
func disambiguate(stats []sensors.TemperatureStat) []namedStat {
	out := make([]namedStat, len(stats))
	used := make(map[string]bool, len(stats))
	next := make(map[string]int, len(stats))
	for i, st := range stats {
		name := st.SensorKey
		if name != "" && used[name] {
			n := next[st.SensorKey]
			if n < 2 {
				n = 2
			}
			for used[st.SensorKey+"#"+strconv.Itoa(n)] {
				n++
			}
			name = st.SensorKey + "#" + strconv.Itoa(n)
			next[st.SensorKey] = n + 1
		}
		used[name] = true
		out[i] = namedStat{TemperatureStat: st, name: name}
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

var _ checks.StatefulPlugin = (*temperaturePlugin)(nil)
