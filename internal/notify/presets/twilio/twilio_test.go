package twilio_test

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
	"github.com/noixdev/pulsix/internal/notify/presets/twilio"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

const (
	sid   = "AC0123456789abcdef0123456789abcdef"
	token = "tok0123456789abcdef0123456789abc"
)

var settings = notify.PresetSettings{AccountSID: sid, From: "+15005550006", To: "+4915112345678"}

func target() notify.Target {
	return notify.Target{Settings: settings, Secrets: map[string]string{"auth_token": token}}
}

func render(t *testing.T, ev notify.Event) (notify.Request, url.Values) {
	t.Helper()
	reqs, err := twilio.New("").Render(ev, target())
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].ContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("requests = %+v", reqs)
	}
	v, err := url.ParseQuery(string(reqs[0].Body))
	if err != nil {
		t.Fatal(err)
	}
	return reqs[0], v
}

func TestTwilioGolden(t *testing.T) {
	for _, c := range notifytest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			got, err := notifytest.Dump(twilio.New("").Render(c.Event, target()))
			if err != nil {
				t.Fatal(err)
			}
			notifytest.Golden(t, filepath.Join("testdata", c.Name+".golden.json"), got, *update)
		})
	}
}

func TestTwilioBasicAuth(t *testing.T) {
	r, v := render(t, notifytest.Test())
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(sid+":"+token))
	if len(r.Header) != 1 || r.Header[0] != [2]string{"Authorization", want} {
		t.Fatalf("header = %v", r.Header)
	}
	if v.Get("To") != settings.To || v.Get("From") != settings.From {
		t.Fatalf("form = %v", v)
	}
}

// isGSM reports whether s stays in the GSM-7 alphabet's ASCII part and
// its letters with diacritics the fixtures use.
func isGSM(s string) bool {
	for _, r := range s {
		if r >= 0x80 && !strings.ContainsRune("äöüÄÖÜß£¥èéùìòÇØøÅåÆæÉ¡¿ñÑà§", r) || r == '`' {
			return false
		}
	}
	return true
}

// TestTwilioFold: Compose's look-alikes and typography become GSM-7
// characters, so an ordinary message keeps the GSM-7 room.
func TestTwilioFold(t *testing.T) {
	for _, ev := range []notify.Event{notifytest.AlertsMixed(), notifytest.Hostile(), notifytest.RunFailed()} {
		_, v := render(t, ev)
		body := v.Get("Body")
		if !isGSM(body) {
			t.Errorf("not GSM-7: %q", body)
		}
		if utf8.RuneCountInString(body) > 306 {
			t.Errorf("body is %d characters", utf8.RuneCountInString(body))
		}
	}
	if _, v := render(t, notifytest.AlertsMixed()); !strings.Contains(v.Get("Body"), "crit - opened") || !strings.Contains(v.Get("Body"), "200 - saw 503") {
		t.Fatalf("body = %q", v.Get("Body"))
	}
}

// TestTwilioBodyBounds: at most two segments in either encoding, with a
// note for what was left out.
func TestTwilioBodyBounds(t *testing.T) {
	_, v := render(t, notifytest.LargeBatch(50, 7))
	body := v.Get("Body")
	if !isGSM(body) || utf8.RuneCountInString(body) > 306 {
		t.Fatalf("GSM body is %d characters: %q", utf8.RuneCountInString(body), body)
	}
	if !strings.Contains(body, "... and ") {
		t.Fatalf("no note: %q", body)
	}
	for name, ev := range map[string]notify.Event{"digest": notifytest.WideDigest(), "alerts": notifytest.WideBatch()} {
		_, v := render(t, ev)
		body := v.Get("Body")
		if n := len(utf16.Encode([]rune(body))); n > 134 {
			t.Errorf("%s: UCS-2 body is %d units: %q", name, n, body)
		}
		if !strings.Contains(body, "... and ") {
			t.Errorf("%s: no note: %q", name, body)
		}
	}
	long := notifytest.RunFailed()
	long.Schedule.Name = strings.Repeat("x", 300)
	if _, v := render(t, long); utf8.RuneCountInString(v.Get("Body")) > 306 || !strings.HasPrefix(v.Get("Body"), "xxx") {
		t.Fatalf("long title = %q", v.Get("Body"))
	}
}

func TestTwilioSettings(t *testing.T) {
	cases := []struct {
		name string
		s    notify.PresetSettings
		want string
	}{
		{"ok", settings, ""},
		{"sender id", notify.PresetSettings{AccountSID: sid, From: "Pulsix Ops", To: "+4915112345678"}, ""},
		{"spaces trimmed", notify.PresetSettings{AccountSID: " " + sid, From: "+15005550006 ", To: " +4915112345678"}, ""},
		{"bad sid", notify.PresetSettings{AccountSID: "AC123", From: "+15005550006", To: "+4915112345678"}, "account SID"},
		{"no to", notify.PresetSettings{AccountSID: sid, From: "+15005550006"}, "recipient"},
		{"to list", notify.PresetSettings{AccountSID: sid, From: "+15005550006", To: "+4915112345678,+4915112345679"}, "recipient"},
		{"local to", notify.PresetSettings{AccountSID: sid, From: "+15005550006", To: "015112345678"}, "recipient"},
		{"digits sender id", notify.PresetSettings{AccountSID: sid, From: "12345", To: "+4915112345678"}, "sender"},
		{"long sender id", notify.PresetSettings{AccountSID: sid, From: "PulsixOpsTeam", To: "+4915112345678"}, "sender"},
		{"chat id", notify.PresetSettings{AccountSID: sid, From: "+15005550006", To: "+4915112345678", ChatID: "1"}, "chat_id"},
	}
	for _, c := range cases {
		s, err := twilio.New("").CheckSettings(c.s)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v", c.name, err)
		}
		if err == nil && s.AccountSID != sid {
			t.Errorf("%s: not trimmed: %+v", c.name, s)
		}
	}
}

func TestTwilioEndpointAndSecret(t *testing.T) {
	ep := twilio.New("").(notify.Endpointed)
	if got := ep.Endpoint(settings); got != "https://api.twilio.com/2010-04-01/Accounts/"+sid+"/Messages.json" {
		t.Fatalf("endpoint = %s", got)
	}
	if got := twilio.New("http://127.0.0.1:9/").(notify.Endpointed).Endpoint(settings); !strings.HasPrefix(got, "http://127.0.0.1:9/2010-04-01/") {
		t.Fatalf("injected endpoint = %s", got)
	}
	c := twilio.New("").(notify.Credentialed)
	if fmt.Sprint(c.SecretNames()) != "[auth_token]" || c.CheckSecret("auth_token", token) != nil {
		t.Fatal("secret")
	}
	for _, bad := range []string{"S3CRET", "S3CRET" + strings.Repeat("a", 27), strings.Repeat("a", 33)} {
		if err := c.CheckSecret("auth_token", bad); err == nil || strings.Contains(err.Error(), "S3CRET") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// TestTwilioSecretBelongsToTheAccount (review M2): the auth token is
// scoped to the account SID, so a new SID needs it again.
func TestTwilioSecretBelongsToTheAccount(t *testing.T) {
	sc, ok := twilio.New("").(notify.SecretScoped)
	if !ok {
		t.Fatal("not scoped")
	}
	if name, value := sc.SecretScope(settings); name != "account_sid" || value != sid {
		t.Fatalf("scope = %s %s", name, value)
	}
}
