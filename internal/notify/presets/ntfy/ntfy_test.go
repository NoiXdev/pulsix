package ntfy_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/ntfy"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

type payload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click"`
}

func target(raw string) notify.Target {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return notify.Target{URL: u}
}

func render(t *testing.T, ev notify.Event, raw string) (notify.Request, payload) {
	t.Helper()
	reqs, err := ntfy.New().Render(ev, target(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var p payload
	if err := json.Unmarshal(reqs[0].Body, &p); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, reqs[0].Body)
	}
	return reqs[0], p
}

func TestNtfyGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			got, err := notifytest.Dump(ntfy.New().Render(c.Event, target("https://ntfy.example/pulsix-alerts")))
			if err != nil {
				t.Fatal(err)
			}
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), got, *update)
		})
	}
}

// TestNtfyPublishesToTheRoot: the JSON publish goes to the server's root
// (the topic URL's parent, also under a path prefix) and names the topic.
func TestNtfyPublishesToTheRoot(t *testing.T) {
	for raw, root := range map[string]string{
		"https://ntfy.sh/alerts_1":               "/",
		"https://example.com/push/ntfy/alerts-2": "/push/ntfy/",
	} {
		r, p := render(t, notifytest.RunFailed(), raw)
		if r.Path != root || p.Topic != raw[strings.LastIndex(raw, "/")+1:] || r.ContentType != "" {
			t.Errorf("%s: path %q, topic %q", raw, r.Path, p.Topic)
		}
	}
}

func TestNtfyPriorityAndTags(t *testing.T) {
	cases := map[string]struct {
		ev   notify.Event
		prio int
		tag  string
	}{
		"crit":   {notifytest.RunFailed(), 5, "rotating_light"},
		"warn":   {notifytest.ScheduleSkipped(), 4, "warning"},
		"ok":     {notifytest.AlertsClosed(), 2, "white_check_mark"},
		"info":   {notifytest.Test(), 3, "information_source"},
		"crit 2": {notifytest.AlertsMixed(), 5, "rotating_light"},
	}
	for name, c := range cases {
		_, p := render(t, c.ev, "https://ntfy.sh/t")
		if p.Priority != c.prio || len(p.Tags) != 1 || p.Tags[0] != c.tag {
			t.Errorf("%s: priority %d, tags %v", name, p.Priority, p.Tags)
		}
	}
	if _, p := render(t, notifytest.RunFailed(), "https://ntfy.sh/t"); p.Click != notifytest.RunFailed().URL {
		t.Errorf("click = %q", p.Click)
	}
	if _, p := render(t, notifytest.Hostile(), "https://ntfy.sh/t"); strings.Contains(p.Message, "@everyone") || strings.Contains(p.Message, "\u202E") {
		t.Errorf("hostile message = %q", p.Message)
	}
}

func TestNtfyCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://ntfy.sh/pulsix-alerts":              true,
		"https://push.example.com/a/b/x_Y-1":         true,
		"https://ntfy.sh/":                           false,
		"https://ntfy.sh":                            false,
		"https://ntfy.sh/topic/":                     false,
		"https://ntfy.sh/topic?auth=S3CRET":          false,
		"https://ntfy.sh/bad.topic":                  false,
		"https://ntfy.sh/" + strings.Repeat("a", 65): false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		err = ntfy.New().CheckURL(u)
		if (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
		if err != nil && (strings.Contains(err.Error(), "S3CRET") || strings.Contains(err.Error(), "topic?")) {
			t.Errorf("%s: the error repeats the URL: %v", raw, err)
		}
	}
	if _, err := ntfy.New().CheckSettings(notify.PresetSettings{ChatID: "1"}); err == nil {
		t.Error("a chat id is accepted")
	}
}

// TestNtfyBounds: the message stays within ntfy's 4096 bytes, says what it
// left out, and the widest events stay far under the body cap.
func TestNtfyBounds(t *testing.T) {
	_, p := render(t, notifytest.LargeBatch(50, 7), "https://ntfy.sh/t")
	if len(p.Message) > 4096 {
		t.Fatalf("message is %d bytes", len(p.Message))
	}
	lines := strings.Split(p.Message, "\n")
	shown := len(lines) - 1
	if want := fmt.Sprintf("… and %d more", 50-shown+7); lines[len(lines)-1] != want || shown == 0 {
		t.Fatalf("note %q after %d lines, want %q", lines[len(lines)-1], shown, want)
	}
	for name, ev := range map[string]notify.Event{"digest": notifytest.WideDigest(), "alerts": notifytest.WideBatch()} {
		r, p := render(t, ev, "https://ntfy.sh/t")
		if len(r.Body) > 60<<10 || len(p.Message) > 4096 || !strings.Contains(p.Message, "… and ") {
			t.Errorf("%s: body %d bytes, message %d bytes", name, len(r.Body), len(p.Message))
		}
	}
}
