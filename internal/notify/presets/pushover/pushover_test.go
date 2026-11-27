package pushover_test

import (
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/pushover"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

const (
	token = "aToken0123456789aToken01234567"
	user  = "uUser0123456789uUser0123456789"
)

var secrets = map[string]string{"token": token, "user": user}

func render(t *testing.T, ev notify.Event) (notify.Request, url.Values) {
	t.Helper()
	reqs, err := pushover.New("").Render(ev, notify.Target{Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].ContentType != "application/x-www-form-urlencoded" || reqs[0].Path != "" {
		t.Fatalf("requests = %+v", reqs)
	}
	v, err := url.ParseQuery(string(reqs[0].Body))
	if err != nil {
		t.Fatal(err)
	}
	return reqs[0], v
}

func TestPushoverGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			got, err := notifytest.Dump(pushover.New("").Render(c.Event, notify.Target{Secrets: secrets}))
			if err != nil {
				t.Fatal(err)
			}
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), got, *update)
		})
	}
}

func TestPushoverFields(t *testing.T) {
	_, v := render(t, notifytest.RunFailed())
	if v.Get("token") != token || v.Get("user") != user || v.Get("html") != "" || v.Get("url") != notifytest.RunFailed().URL ||
		v.Get("url_title") != notify.OpenLabel || v.Get("timestamp") != fmt.Sprint(notifytest.At.Unix()) {
		t.Fatalf("fields = %v", v)
	}
	for name, c := range map[string]struct {
		ev   notify.Event
		prio string
	}{
		"crit": {notifytest.RunFailed(), "1"}, "warn": {notifytest.ScheduleSkipped(), "0"},
		"ok": {notifytest.AlertsClosed(), "-1"}, "info": {notifytest.Test(), "-1"},
	} {
		if _, v := render(t, c.ev); v.Get("priority") != c.prio {
			t.Errorf("%s: priority %q", name, v.Get("priority"))
		}
	}
	// A link Pushover would refuse (over 512 characters) is left out.
	ev := notifytest.RunFailed()
	ev.URL = notifytest.LongBase + "/runs/x"
	if _, v := render(t, ev); v.Has("url") || v.Has("url_title") {
		t.Fatalf("long url kept: %v", v)
	}
}

// TestPushoverBounds: title ≤ 250 and message ≤ 1024 characters, with a
// note for what was left out; never an empty message.
func TestPushoverBounds(t *testing.T) {
	_, v := render(t, notifytest.LargeBatch(50, 7))
	msg := v.Get("message")
	if n := utf8.RuneCountInString(msg); n > 1024 {
		t.Fatalf("message is %d characters", n)
	}
	lines := strings.Split(msg, "\n")
	shown := len(lines) - 1
	if want := fmt.Sprintf("… and %d more", 50-shown+7); lines[shown] != want || shown == 0 {
		t.Fatalf("note %q after %d lines, want %q", lines[shown], shown, want)
	}
	ev := notifytest.RunFailed()
	ev.Schedule.Name = strings.Repeat("ß", 400)
	if _, v := render(t, ev); utf8.RuneCountInString(v.Get("title")) > 250 {
		t.Fatalf("title is %d characters", utf8.RuneCountInString(v.Get("title")))
	}
	for name, ev := range map[string]notify.Event{"digest": notifytest.WideDigest(), "alerts": notifytest.WideBatch()} {
		r, v := render(t, ev)
		if len(r.Body) > 60<<10 || utf8.RuneCountInString(v.Get("message")) > 1024 || !strings.Contains(v.Get("message"), "… and ") {
			t.Errorf("%s: body %d bytes", name, len(r.Body))
		}
	}
	empty := notify.Event{Kind: "unknown.kind", Org: notify.OrgRef{Name: "Acme"}}
	if _, v := render(t, empty); v.Get("message") == "" {
		t.Fatal("empty message")
	}
}

func TestPushoverEndpointAndSecrets(t *testing.T) {
	p := pushover.New("")
	ep, ok := p.(notify.Endpointed)
	if !ok || ep.Endpoint(notify.PresetSettings{}) != "https://api.pushover.net/1/messages.json" {
		t.Fatal("endpoint")
	}
	if ep := pushover.New("http://127.0.0.1:9").(notify.Endpointed); ep.Endpoint(notify.PresetSettings{}) != "http://127.0.0.1:9/1/messages.json" {
		t.Fatal("injected endpoint")
	}
	c := p.(notify.Credentialed)
	if strings.Join(c.SecretNames(), ",") != "token,user" {
		t.Fatalf("names = %v", c.SecretNames())
	}
	for _, bad := range []string{"S3CRET", strings.Repeat("a", 29), strings.Repeat("a", 31), strings.Repeat("ä", 30), "S3CRET" + strings.Repeat("-", 24)} {
		for _, name := range []string{"token", "user"} {
			err := c.CheckSecret(name, bad)
			if err == nil || strings.Contains(err.Error(), "S3CRET") {
				t.Errorf("%s %q: %v", name, bad, err)
			}
		}
	}
	if c.CheckSecret("token", token) != nil || c.CheckSecret("user", user) != nil {
		t.Fatal("good secrets refused")
	}
	if _, err := p.CheckSettings(notify.PresetSettings{To: "+491"}); err == nil {
		t.Fatal("a setting is accepted")
	}
}
