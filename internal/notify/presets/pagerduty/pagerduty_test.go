package pagerduty_test

import (
	"encoding/json"
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/pagerduty"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

const key = "R0123456789abcdef0123456789abcde"

func target() notify.Target {
	return notify.Target{Settings: notify.PresetSettings{Region: "us"}, Secrets: map[string]string{"routing_key": key}}
}

type sent struct {
	Path        string
	RoutingKey  string `json:"routing_key"`
	EventAction string `json:"event_action"`
	DedupKey    string `json:"dedup_key"`
	Payload     *struct {
		Summary       string `json:"summary"`
		Source        string `json:"source"`
		Severity      string `json:"severity"`
		CustomDetails struct {
			Lines   []string `json:"lines"`
			Omitted int      `json:"omitted"`
		} `json:"custom_details"`
	} `json:"payload"`
	Links []struct {
		Href string `json:"href"`
	} `json:"links"`
}

func render(t *testing.T, ev notify.Event) []sent {
	t.Helper()
	reqs, err := pagerduty.New("").Render(ev, target())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]sent, len(reqs))
	for i, r := range reqs {
		if r.ContentType != "" || len(r.Header) > 0 || len(r.Body) > 60<<10 {
			t.Fatalf("request %d = %+v", i, r)
		}
		if err := json.Unmarshal(r.Body, &out[i]); err != nil {
			t.Fatalf("body is not JSON: %v\n%s", err, r.Body)
		}
		out[i].Path = r.Path
	}
	return out
}

func TestPagerDutyGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			got, err := notifytest.Dump(pagerduty.New("").Render(c.Event, target()))
			if err != nil {
				t.Fatal(err)
			}
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), got, *update)
		})
	}
}

// TestPagerDutyActions: one event per alert on its own incident; closed
// resolves, acknowledged and snoozed acknowledge, the rest trigger.
func TestPagerDutyActions(t *testing.T) {
	ev := notifytest.AlertsMixed()
	ev.Alerts.Items = append(ev.Alerts.Items, ev.Alerts.Items[0], ev.Alerts.Items[0])
	ev.Alerts.Items[4].Event, ev.Alerts.Items[5].Event = "acknowledged", "snoozed"
	got := render(t, ev)
	want := []struct{ action, severity string }{
		{"trigger", "critical"}, {"trigger", "warning"}, {"resolve", ""}, {"trigger", "warning"}, {"acknowledge", ""}, {"acknowledge", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("%d events", len(got))
	}
	for i, w := range want {
		g := got[i]
		if g.EventAction != w.action || g.RoutingKey != key || g.DedupKey != "pulsix:alert:"+ev.Alerts.Items[i].AlertID.String() || g.Path != "" {
			t.Errorf("event %d = %+v", i, g)
		}
		if (w.severity == "") != (g.Payload == nil) || (g.Payload != nil && g.Payload.Severity != w.severity) {
			t.Errorf("event %d payload = %+v", i, g.Payload)
		}
	}
	if p := got[0].Payload; !strings.HasPrefix(p.Summary, "shop-1: crit · opened") || p.Source != "shop-1" || len(got[0].Links) != 1 {
		t.Fatalf("trigger = %+v", got[0])
	}
	if p := got[3].Payload; p.Source != "edge-3" {
		t.Fatalf("absent source = %q", p.Source)
	}
	// A batch over the cap sends at most 50 events.
	if n := len(render(t, notifytest.LargeBatch(60, 0))); n != 50 {
		t.Fatalf("%d events for 60 alerts", n)
	}
}

// TestPagerDutyNonAlertEvents: a failure triggers on its run, a skipped
// schedule warns on its occurrence, and good news, the digest and the
// test are change events that page nobody.
func TestPagerDutyNonAlertEvents(t *testing.T) {
	run := render(t, notifytest.RunFailed())
	if len(run) != 1 || run[0].EventAction != "trigger" || run[0].Payload.Severity != "error" ||
		run[0].DedupKey != "pulsix:run:"+notifytest.RunFailed().Run.ID.String() || run[0].Path != "" {
		t.Fatalf("run = %+v", run)
	}
	skip := render(t, notifytest.ScheduleSkipped())
	if skip[0].EventAction != "trigger" || skip[0].Payload.Severity != "warning" || !strings.HasPrefix(skip[0].DedupKey, "pulsix:schedule:"+notifytest.ScheduleSkipped().Schedule.ID.String()+":") {
		t.Fatalf("skip = %+v", skip)
	}
	for name, ev := range map[string]notify.Event{"succeeded": notifytest.RunSucceeded(), "digest": notifytest.Digest(), "test": notifytest.Test()} {
		got := render(t, ev)
		if len(got) != 1 || got[0].Path != "/v2/change/enqueue" || got[0].EventAction != "" || got[0].RoutingKey != key || got[0].Payload.Summary == "" {
			t.Errorf("%s = %+v", name, got)
		}
	}
}

func TestPagerDutyRegion(t *testing.T) {
	p := pagerduty.New("")
	for in, want := range map[string]string{"": "us", "EU": "eu", "us": "us"} {
		s, err := p.CheckSettings(notify.PresetSettings{Region: in})
		if err != nil || s.Region != want {
			t.Errorf("%q: %+v %v", in, s, err)
		}
	}
	if _, err := p.CheckSettings(notify.PresetSettings{Region: "apac"}); err == nil {
		t.Error("apac accepted")
	}
	if _, err := p.CheckSettings(notify.PresetSettings{To: "+491"}); err == nil {
		t.Error("a setting is accepted")
	}
	ep := p.(notify.Endpointed)
	if ep.Endpoint(notify.PresetSettings{Region: "us"}) != "https://events.pagerduty.com/v2/enqueue" ||
		ep.Endpoint(notify.PresetSettings{Region: "eu"}) != "https://events.eu.pagerduty.com/v2/enqueue" ||
		pagerduty.New("http://127.0.0.1:9").(notify.Endpointed).Endpoint(notify.PresetSettings{Region: "eu"}) != "http://127.0.0.1:9/v2/enqueue" {
		t.Fatal("endpoints")
	}
	c := p.(notify.Credentialed)
	if c.CheckSecret("routing_key", key) != nil {
		t.Fatal("good key refused")
	}
	for _, bad := range []string{"S3CRET", "S3CRET" + strings.Repeat("a", 25), strings.Repeat("-", 32)} {
		if err := c.CheckSecret("routing_key", bad); err == nil || strings.Contains(err.Error(), "S3CRET") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// TestPagerDutyBounds: summaries stay within 1024 characters and the
// widest events far under the body cap, saying what they left out.
func TestPagerDutyBounds(t *testing.T) {
	for _, e := range render(t, notifytest.WideBatch()) {
		if utf8.RuneCountInString(e.Payload.Summary) > 1024 {
			t.Fatalf("summary is %d characters", utf8.RuneCountInString(e.Payload.Summary))
		}
	}
	d := render(t, notifytest.WideDigest())
	if d[0].Payload.CustomDetails.Omitted == 0 || len(d[0].Payload.CustomDetails.Lines) == 0 {
		t.Fatalf("digest details = %d lines, %d omitted", len(d[0].Payload.CustomDetails.Lines), d[0].Payload.CustomDetails.Omitted)
	}
	h := render(t, notifytest.Hostile())
	if s := h[0].Payload.Summary; strings.Contains(s, "@everyone") || strings.Contains(s, "\u202E") || len(h[0].Links) != 0 {
		t.Fatalf("hostile = %+v", h[0])
	}
}
