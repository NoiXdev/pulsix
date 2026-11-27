package notifytest_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/notifytest"
)

func TestCasesAreDistinctAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range notifytest.Cases() {
		if seen[c.Name] || c.Event.Kind == "" || c.Event.Org.Name == "" {
			t.Fatalf("case %q", c.Name)
		}
		seen[c.Name] = true
	}
	if ev := notifytest.LargeBatch(3, 2); ev.Alerts.Count != 5 || len(ev.Alerts.Items) != 3 {
		t.Fatalf("large batch = %+v", ev.Alerts)
	}
}

func TestGoldenWritesIndentedJSONAndCompares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.golden.json")
	notifytest.Golden(t, path, []byte(`{"a":1}`), true)
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "\n  \"a\": 1\n") {
		t.Fatalf("written %q, %v", b, err)
	}
	notifytest.Golden(t, path, []byte(`{"a":1}`), false)
	text := filepath.Join(t.TempDir(), "plain.golden")
	notifytest.Golden(t, text, []byte("not json"), true)
	notifytest.Golden(t, text, []byte("not json"), false)
}

func TestBodyWantsOnePlainJSONRequest(t *testing.T) {
	if b, err := notifytest.Body([]notify.Request{{Body: []byte(`{}`)}}, nil); err != nil || string(b) != "{}" {
		t.Fatalf("one = %s, %v", b, err)
	}
	for name, reqs := range map[string][]notify.Request{
		"two":    {{Body: []byte(`{}`)}, {Body: []byte(`{}`)}},
		"path":   {{Path: "/x", Body: []byte(`{}`)}},
		"form":   {{ContentType: "application/x-www-form-urlencoded"}},
		"header": {{Header: [][2]string{{"A", "b"}}}},
	} {
		if _, err := notifytest.Body(reqs, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := notifytest.Body(nil, errors.New("boom")); err == nil {
		t.Error("error swallowed")
	}
}

func TestDumpShowsJSONAndFormBodies(t *testing.T) {
	got, err := notifytest.Dump([]notify.Request{
		{Path: "/a", Body: []byte(`{"x":"<b>"}`)},
		{ContentType: "application/x-www-form-urlencoded", Header: [][2]string{{"Authorization", "Basic x"}}, Body: []byte("b=2&a=%3C1%3E")},
	}, nil)
	want := `[{"path":"/a","json":{"x":"<b>"}},{"content_type":"application/x-www-form-urlencoded","header":[["Authorization","Basic x"]],"form":{"a":"<1>","b":"2"}}]`
	if err != nil || string(got) != want {
		t.Fatalf("dump = %s, %v", got, err)
	}
	for name, r := range map[string]notify.Request{
		"bad json": {Body: []byte("{")},
		"bad form": {ContentType: "application/x-www-form-urlencoded", Body: []byte("%zz")},
		"other":    {ContentType: "text/plain", Body: []byte("x")},
	} {
		if _, err := notifytest.Dump([]notify.Request{r}, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := notifytest.Dump(nil, errors.New("boom")); err == nil {
		t.Error("error swallowed")
	}
}

func TestWideFixturesAreFull(t *testing.T) {
	if d := notifytest.WideDigest().Digest; len(d.Schedules) != notify.MaxDigestSchedules || !d.Truncated || len(d.Slowest) != 6 {
		t.Fatalf("wide digest = %d schedules", len(d.Schedules))
	}
	if b := notifytest.WideBatch().Alerts; len(b.Items) != 50 || b.Count != 50 {
		t.Fatalf("wide batch = %d items", len(b.Items))
	}
}
