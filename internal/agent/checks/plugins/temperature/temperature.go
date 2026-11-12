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
package temperature

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

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
type temperaturePlugin struct {
	Sensors sensorReader
}

// New returns the temperature plugin wired to gopsutil.
func New() checks.Plugin {
	return temperaturePlugin{Sensors: gopsutilSensors{}}
}

func (temperaturePlugin) Name() string { return "temperature" }

const noSensors = "no temperature sensors found"

func (p temperaturePlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
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

	if len(obs) == 0 {
		msg := errors.New(noSensors)
		errs = append(errs, msg)
		return []checks.Observation{checks.ErrObservation("", "temp.celsius", errors.Join(errs...))}, errors.Join(errs...)
	}
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

var _ checks.Plugin = temperaturePlugin{}
