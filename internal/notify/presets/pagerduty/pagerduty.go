// Package pagerduty renders a webhook channel's message for PagerDuty's
// Events API v2 (backlog 4.8). Pulsix builds the URL from the region; the
// integration's routing key is the channel's sealed secret. Alerts open,
// acknowledge and resolve one incident each through a stable dedup key;
// failures trigger; everything else is a change event, which pages
// nobody.
package pagerduty

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/notify"
)

const (
	hostUS = "https://events.pagerduty.com"
	hostEU = "https://events.eu.pagerduty.com"
	// enqueuePath takes alert events, changePath change events.
	enqueuePath = "/v2/enqueue"
	changePath  = "/v2/change/enqueue"
	// summaryLimit is PagerDuty's limit on a summary, in characters.
	summaryLimit = 1024
	// maxEvents bounds the events one alert batch sends: the batch's own
	// cap (alertroutes.MaxBatchItems).
	maxEvents = 50
)

var routingKey = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)

type preset struct{ base string }

// New returns the PagerDuty preset; base "" posts to PagerDuty's own
// host for the channel's region, anything else (tests) to base.
func New(base string) notify.Preset { return preset{base: strings.TrimRight(base, "/")} }

// CheckURL is never asked: the preset builds its URL.
func (preset) CheckURL(*url.URL) error { return nil }

// CheckSettings normalises the region: us (the default) or eu.
func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	switch s.Region = strings.ToLower(strings.TrimSpace(s.Region)); s.Region {
	case "":
		s.Region = "us"
	case "us", "eu":
	default:
		return s, errors.New("a PagerDuty region is us or eu")
	}
	return s, s.Only("PagerDuty", "region")
}

// Endpoint is the region's alert events URL.
func (p preset) Endpoint(s notify.PresetSettings) string {
	switch {
	case p.base != "":
		return p.base + enqueuePath
	case s.Region == "eu":
		return hostEU + enqueuePath
	}
	return hostUS + enqueuePath
}

// SecretNames is the integration's routing key.
func (preset) SecretNames() []string { return []string{"routing_key"} }

// CheckSecret checks the shape; its error never repeats the value.
func (preset) CheckSecret(_, value string) error {
	if !routingKey.MatchString(value) {
		return errors.New("a PagerDuty integration key is 32 letters and digits")
	}
	return nil
}

type link struct {
	Href string `json:"href"`
	Text string `json:"text"`
}

type details struct {
	Lines   []string `json:"lines"`
	Omitted int      `json:"omitted,omitempty"`
}

type payload struct {
	Summary       string   `json:"summary"`
	Source        string   `json:"source"`
	Severity      string   `json:"severity,omitempty"`
	Timestamp     string   `json:"timestamp,omitempty"`
	Component     string   `json:"component,omitempty"`
	Group         string   `json:"group,omitempty"`
	Class         string   `json:"class,omitempty"`
	CustomDetails *details `json:"custom_details,omitempty"`
}

// event is an alert event; payload, client and links only on a trigger.
type event struct {
	RoutingKey  string   `json:"routing_key"`
	EventAction string   `json:"event_action"`
	DedupKey    string   `json:"dedup_key"`
	Payload     *payload `json:"payload,omitempty"`
	Client      string   `json:"client,omitempty"`
	ClientURL   string   `json:"client_url,omitempty"`
	Links       []link   `json:"links,omitempty"`
}

type change struct {
	RoutingKey string  `json:"routing_key"`
	Payload    payload `json:"payload"`
	Links      []link  `json:"links,omitempty"`
}

// Render is one alert event per alert of a batch, one trigger for a
// failure or a warning, and one change event for everything else.
func (preset) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	key := t.Secrets["routing_key"]
	if ev.Kind == notify.EventAlertBatch && ev.Alerts != nil && len(ev.Alerts.Items) > 0 {
		return alertEvents(ev, key)
	}
	m := notify.Compose(ev)
	lines, omitted := detailLines(m)
	p := payload{
		Summary: notify.Clip(m.Title, summaryLimit), Source: source(m.Org), Timestamp: stamp(ev.At),
		CustomDetails: &details{Lines: lines, Omitted: omitted},
	}
	var links []link
	if m.URL != "" {
		links = []link{{Href: m.URL, Text: notify.OpenLabel}}
	}
	if m.Level != notify.LevelCrit && m.Level != notify.LevelWarn {
		body, err := marshal(change{RoutingKey: key, Payload: p, Links: links})
		return []notify.Request{{Path: changePath, Body: body}}, err
	}
	p.Severity, p.Group = "warning", m.Org
	if m.Level == notify.LevelCrit {
		p.Severity = "error"
	}
	body, err := marshal(event{
		RoutingKey: key, EventAction: "trigger", DedupKey: dedupKey(ev), Payload: &p,
		Client: "Pulsix", ClientURL: m.URL, Links: links,
	})
	return []notify.Request{{Body: body}}, err
}

// alertEvents maps each alert to an event on its own incident: closed
// resolves, acknowledged and snoozed acknowledge, every other event
// (opened, worsened, escalated, unacknowledged, unsnoozed) triggers.
func alertEvents(ev notify.Event, key string) ([]notify.Request, error) {
	items := ev.Alerts.Items
	if len(items) > maxEvents {
		items = items[:maxEvents]
	}
	out := make([]notify.Request, 0, len(items))
	for _, it := range items {
		e := event{RoutingKey: key, DedupKey: "pulsix:alert:" + it.AlertID.String()}
		switch it.Event {
		case "closed":
			e.EventAction = "resolve"
		case "acknowledged", "snoozed":
			e.EventAction = "acknowledge"
		default:
			e.EventAction = "trigger"
			// The item as a one-alert batch: the same line every other
			// preset shows for it, defused and clipped.
			one := ev
			one.Alerts = &notify.AlertBatch{Count: 1, Items: []notify.AlertItem{it}}
			m := notify.Compose(one)
			l := m.Lines[0]
			severity := "warning"
			if it.Severity == "crit" {
				severity = "critical"
			}
			src := l.Label
			if strings.TrimSpace(src) == "" {
				src = m.Org
			}
			e.Payload = &payload{
				Summary: notify.Clip(l.Label+": "+l.Text, summaryLimit), Source: source(src), Severity: severity,
				Timestamp: stamp(it.At), Component: notify.ChatText(it.CheckKey), Group: m.Org, Class: notify.ChatText(it.Metric),
				CustomDetails: &details{Lines: []string{l.Text}},
			}
			e.Client, e.ClientURL = "Pulsix", m.URL
			if l.URL != "" {
				e.Links = []link{{Href: l.URL, Text: notify.OpenLabel}}
			}
		}
		body, err := marshal(e)
		if err != nil {
			return nil, err
		}
		out = append(out, notify.Request{Body: body})
	}
	return out, nil
}

// detailLines are the message's lines as "label: text", within the byte
// budget every renderer keeps, and the count left out.
func detailLines(m notify.Message) ([]string, int) {
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = l.Text
		if l.Label != "" {
			lines[i] = l.Label + ": " + l.Text
		}
	}
	// Each line costs its JSON string plus quotes and a comma.
	return notify.FitWithin(lines, m.Omitted, notify.Budget{Limit: notify.MaxRenderedLines, Size: func(s string) int { return notify.JSONSize(s) + 2 }})
}

// dedupKey names the incident a failure or warning opens: per run, or per
// schedule occurrence.
func dedupKey(ev notify.Event) string {
	switch {
	case ev.Run != nil:
		return "pulsix:run:" + ev.Run.ID.String()
	case ev.Schedule != nil && ev.Decision != nil:
		return "pulsix:schedule:" + ev.Schedule.ID.String() + ":" + strconv.FormatInt(ev.Decision.DueAt.Unix(), 10)
	}
	return "pulsix:" + string(ev.Kind) + ":" + ev.Org.ID.String() + ":" + strconv.FormatInt(ev.At.Unix(), 10)
}

// source is what PagerDuty requires as the affected system.
func source(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "Pulsix"
	}
	return s
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// marshal encodes without Go's HTML escaping, so the body reads as sent.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
