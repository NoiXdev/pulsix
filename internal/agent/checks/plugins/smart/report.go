package smart

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// smartctl's exit status is a bit mask (smartctl(8), EXIT STATUS). Bit 0:
// the command line did not parse. Bit 1: the device could not be opened,
// or (with a plain -n) it was in a low-power mode; the plugin passes
// -n standby,0, so a skipped disk exits 0 and bit 1 always means "not
// opened". Bit 2: a SMART or other command to the disk failed, or a SMART
// data structure had a checksum error. Bits 3–7 are SMART findings — disk
// failing, prefail or usage attributes at or past their threshold now or
// in the past, error log entries, self-test errors — and are data: the
// JSON is read as usual.
const (
	exitCommandLine = 1 << 0
	exitOpenFailed  = 1 << 1
)

// report is the part of `smartctl --json -a` the plugin reads. Every field
// is optional: a USB bridge without SMART pass-through, a virtual disk or
// an older smartctl leave blocks out, and a metric whose field is absent
// is not reported.
//
//	smart_status.passed                          → smart.passed (1/0)
//	temperature.current                          → smart.temperature_celsius
//	  (else nvme_smart_health_information_log.temperature)
//	power_on_time.hours                          → smart.power_on_hours
//	  (else nvme_smart_health_information_log.power_on_hours)
//	ata_smart_attributes.table[id=5].raw.value   → smart.reallocated_sectors
//	ata_smart_attributes.table[id=197].raw.value → smart.pending_sectors
//	ata_smart_attributes.table[id=198].raw.value → smart.offline_uncorrectable
//	nvme_smart_health_information_log.percentage_used → smart.nvme_percentage_used
//	nvme_smart_health_information_log.available_spare → smart.nvme_available_spare_pct
//	nvme_smart_health_information_log.media_errors    → smart.nvme_media_errors
//
// Numbers decode as float64: NVMe counters are 128-bit and smartctl prints
// them whole. The leaves are optNum/optBool rather than pointers because
// encoding/json allocates a pointer before it finds the value has the wrong
// type — a "passed": "yes" would read as a failed disk.
type report struct {
	Smartctl    smartctlBlock `json:"smartctl"`
	SmartStatus struct {
		Passed optBool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current optNum `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours optNum `json:"hours"`
	} `json:"power_on_time"`
	ATAAttributes struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value optNum `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe struct {
		Temperature    optNum `json:"temperature"`
		PowerOnHours   optNum `json:"power_on_hours"`
		PercentageUsed optNum `json:"percentage_used"`
		AvailableSpare optNum `json:"available_spare"`
		MediaErrors    optNum `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
}

// smartctlBlock carries smartctl's own messages (smartctl.messages[]).
type smartctlBlock struct {
	Messages []struct {
		String   string `json:"string"`
		Severity string `json:"severity"`
	} `json:"messages"`
}

// interpret turns one device's smartctl run into observations: one error
// observation on smart.passed when the run failed (command line refused,
// device not opened, no JSON); otherwise every metric the JSON states, plus
// an advisory error when a field had an unexpected type. A disk in standby
// needs no case of its own: -n standby,0 makes smartctl exit 0 with JSON
// that carries no data blocks, so it reports nothing.
func interpret(d device, res runResult) ([]checks.Observation, error) {
	isJSON := isJSONObject(res.stdout)
	var rep report
	var typeErr error
	if isJSON {
		if err := json.Unmarshal(res.stdout, &rep); err != nil {
			var ute *json.UnmarshalTypeError
			if !errors.As(err, &ute) {
				isJSON = false
			} else {
				typeErr = fmt.Errorf("smart: %s: smartctl output: %w", d.item, err)
			}
		}
	}

	var fail error
	switch {
	case res.exitCode&exitCommandLine != 0:
		fail = fmt.Errorf("smart: %s: smartctl refused the command line (exit status %d)", d.item, res.exitCode)
	case res.exitCode&exitOpenFailed != 0:
		fail = fmt.Errorf("smart: %s: smartctl could not open the device (exit status %d)", d.item, res.exitCode)
	case !isJSON:
		fail = fmt.Errorf("smart: %s: smartctl %s", d.item, noJSON(res.exitCode))
	}
	if fail != nil {
		if isJSON {
			fail = fmt.Errorf("%w%s", fail, rep.Smartctl.detail())
		} else if res.exitCode&exitCommandLine != 0 {
			fail = fmt.Errorf("%w: no JSON output (smartmontools 7.0 or later is needed)", fail)
		}
		return []checks.Observation{checks.ErrObservation(d.item, "smart.passed", fail)}, fail
	}
	obs, badErr := rep.observations(d.item)
	return obs, errors.Join(typeErr, badErr)
}

// observations reports every metric whose field is present, and names in
// the returned error the fields that had an unexpected type.
func (r report) observations(item string) ([]checks.Observation, error) {
	var obs []checks.Observation
	var bad []string
	add := func(metric string, v optNum) {
		switch {
		case v.ok:
			obs = append(obs, checks.ValueObservation(item, metric, v.v))
		case v.bad:
			bad = append(bad, metric)
		}
	}
	passed := optNum{bad: r.SmartStatus.Passed.bad}
	if r.SmartStatus.Passed.ok {
		passed.ok = true
		if r.SmartStatus.Passed.v {
			passed.v = 1
		}
	}
	add("smart.passed", passed)
	add("smart.temperature_celsius", firstOf(r.Temperature.Current, r.NVMe.Temperature))
	add("smart.power_on_hours", firstOf(r.PowerOnTime.Hours, r.NVMe.PowerOnHours))
	for _, a := range []struct {
		id     int
		metric string
	}{{5, "smart.reallocated_sectors"}, {197, "smart.pending_sectors"}, {198, "smart.offline_uncorrectable"}} {
		add(a.metric, r.attribute(a.id))
	}
	add("smart.nvme_percentage_used", r.NVMe.PercentageUsed)
	add("smart.nvme_available_spare_pct", r.NVMe.AvailableSpare)
	add("smart.nvme_media_errors", r.NVMe.MediaErrors)
	if len(bad) > 0 {
		return obs, fmt.Errorf("smart: %s: smartctl output has an unexpected type for %s", item, strings.Join(bad, ", "))
	}
	return obs, nil
}

// attribute is the raw value of the first ATA attribute with that id.
func (r report) attribute(id int) optNum {
	for _, a := range r.ATAAttributes.Table {
		if a.ID == id {
			return a.Raw.Value
		}
	}
	return optNum{}
}

// firstOf is the first present value; else it carries whether any of them
// had the wrong type.
func firstOf(vs ...optNum) optNum {
	var out optNum
	for _, v := range vs {
		if v.ok {
			return v
		}
		out.bad = out.bad || v.bad
	}
	return out
}

// optNum is an optional JSON number: ok when present and a number, bad
// when present with another type; null counts as absent.
type optNum struct {
	v       float64
	ok, bad bool
}

func (o *optNum) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if err := json.Unmarshal(b, &o.v); err != nil {
		o.bad = true
		return nil
	}
	o.ok = true
	return nil
}

// optBool is optNum for a JSON boolean.
type optBool struct {
	v       bool
	ok, bad bool
}

func (o *optBool) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if err := json.Unmarshal(b, &o.v); err != nil {
		o.bad = true
		return nil
	}
	o.ok = true
	return nil
}

// maxDetail bounds how much of smartctl's own messages an error quotes.
const maxDetail = 300

// detail is ": " plus smartctl's error messages (all its messages when none
// is marked as an error), on one line and cut to maxDetail bytes; "" when
// there are none.
func (b smartctlBlock) detail() string {
	var msgs []string
	for _, errorsOnly := range []bool{true, false} {
		for _, m := range b.Messages {
			if (!errorsOnly || m.Severity == "error") && strings.TrimSpace(m.String) != "" {
				msgs = append(msgs, strings.TrimSpace(m.String))
			}
		}
		if len(msgs) > 0 {
			break
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, strings.Join(msgs, "; "))
	if len(s) > maxDetail {
		n := maxDetail
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "…"
	}
	return ": " + s
}

func noJSON(exitCode int) string {
	if exitCode&exitCommandLine != 0 {
		return fmt.Sprintf("did not return JSON (exit status %d; smartmontools 7.0 or later is needed)", exitCode)
	}
	return fmt.Sprintf("did not return JSON (exit status %d)", exitCode)
}

// isJSONObject reports whether b is one syntactically valid JSON object.
func isJSONObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}
