// Run output never carries a revealed secret (final review I4): every value
// the claim names in redact, and every credential secret the claim ships,
// is removed from each event's payload — module results and the display
// text alike, including JSON-escaped multi-line keys — before the event
// leaves the runner. Each ansible event is forwarded once: a mapped event
// carries its display text inside its own payload, and a banner becomes a
// stdout event whose payload is {"text": ...}.
package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

const (
	leakPassword = "Sup3r-Secret-Become"
	leakExtraVar = "extra-var-secret-7f1c"
	leakKeyLine  = "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz"
)

var leakKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" + leakKeyLine + "\nc2gtZWQyNTUxOQAAACBZwYmjjckHwNrA0ODDwuywdHhjLPs5j7kySXjBGmAIqwAA\n-----END OPENSSH PRIVATE KEY-----\n"

// secretFixture writes an event stream whose tasks echo every kind of
// secret: raw, inside a module result, JSON-escaped with its newlines, in
// a banner's display text.
func secretFixture(t *testing.T) string {
	t.Helper()
	line := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		line(map[string]any{"event": "playbook_on_task_start", "stdout": "TASK [print " + leakExtraVar + "]", "event_data": map[string]any{"task": "print"}}),
		line(map[string]any{"event": "runner_on_ok", "stdout": "ok: [app1] => " + leakPassword,
			"event_data": map[string]any{"host": "app1", "task": "leak", "uuid": "noise", "res": map[string]any{
				"msg": "password is " + leakPassword, "stdout": leakKey, "nested": []any{map[string]any{"v": leakExtraVar}},
			}}}),
		line(map[string]any{"event": "playbook_on_stats", "stdout": "PLAY RECAP", "event_data": map[string]any{
			"ok": map[string]any{"app1": 1}, "changed": map[string]any{}, "failures": map[string]any{}, "dark": map[string]any{}, "skipped": map[string]any{},
		}}),
	}
	path := filepath.Join(t.TempDir(), "secret-events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRedactsSecretsAndForwardsEachEventOnce(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, secretFixture(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	// The claim's credential for app1 carries the become password; the
	// extra var and the key come in through the manager's redact list.
	job.Credentials["cred-2"] = secretJSON(t, credentials.Secret{PrivateKey: "app-key-material", Password: leakPassword})
	job.Redact = []string{leakExtraVar, leakKey}
	c := &collector{}
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, runEnv(t), c.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertStates(t, c.states(), []string{"run_start", "stdout", "ok", "stdout", "run_end"})
	for _, ev := range c.events {
		p := ev.GetPayload()
		for _, secret := range []string{leakPassword, leakExtraVar, leakKeyLine, "app-key-material"} {
			if strings.Contains(p, secret) {
				t.Errorf("%s event leaks %q: %s", ev.GetState(), secret, p)
			}
		}
		if !json.Valid([]byte(p)) {
			t.Errorf("%s payload is not JSON after redaction: %s", ev.GetState(), p)
		}
	}
	var banner struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(c.events[1].GetPayload()), &banner); err != nil || !strings.Contains(banner.Text, "TASK [print [redacted]]") {
		t.Fatalf("banner payload = %s, want {\"text\": ...} with the secret redacted", c.events[1].GetPayload())
	}
	var ok struct {
		Stdout    string `json:"stdout"`
		EventData struct {
			Host string         `json:"host"`
			Task string         `json:"task"`
			Res  map[string]any `json:"res"`
			UUID string         `json:"uuid"`
		} `json:"event_data"`
	}
	if err := json.Unmarshal([]byte(c.events[2].GetPayload()), &ok); err != nil {
		t.Fatal(err)
	}
	if ok.Stdout != "ok: [app1] => [redacted]" || ok.EventData.Res["msg"] != "password is [redacted]" || ok.EventData.Host != "app1" {
		t.Fatalf("ok payload = %s", c.events[2].GetPayload())
	}
	if ok.EventData.UUID != "" {
		t.Errorf("the forwarded payload keeps runner bookkeeping it does not need: %s", c.events[2].GetPayload())
	}
}

// TestRedactionHappensBeforeTruncation: the loop truncates an oversized
// payload to its head; the runner has already replaced every secret whole,
// so no prefix of a secret can survive at the cut.
func TestRedactionHappensBeforeTruncation(t *testing.T) {
	secret := "tok-" + strings.Repeat("S", 60)
	red := newRedactor(&workerpb.Job{Redact: []string{secret}})
	// Put the secret right across the loop's 64 KiB head boundary of the
	// payload as the runner would forward it without redaction.
	build := func(pad int) []byte {
		msg := strings.Repeat("x", pad) + secret + strings.Repeat("y", 2<<20)
		line, _ := json.Marshal(map[string]any{"event": "runner_on_ok", "stdout": "", "event_data": map[string]any{"host": "h", "task": "t", "res": map[string]any{"msg": msg}}})
		return line
	}
	probe := build(64 << 10)
	offset := strings.Index(string(probe), secret) - 64<<10 // bytes before the padding
	line := build(64<<10 - offset - 30)
	if at := strings.Index(string(line), secret); at >= 64<<10 || at+len(secret) <= 64<<10 {
		t.Fatalf("fixture: secret at %d does not straddle the head boundary", at)
	}
	ev, _, err := ParseEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	payload := red.eventPayload(ev)
	bounded := worker.BoundEventPayload(payload)
	for n := 8; n <= len(secret); n++ {
		if strings.Contains(bounded, secret[:n]) {
			t.Fatalf("a %d-byte prefix of the secret survived truncation", n)
		}
	}
}
