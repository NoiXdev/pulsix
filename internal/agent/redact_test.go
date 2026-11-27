// Tests for owner decision 4 of backlog 3.132: every current agent.yaml
// secret value of 4 bytes or more is replaced by [secret] before a check
// result, a probe result or a fact leaves the agent — whether it goes out
// live or waits in the disconnect buffer.
package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/logring"
	"github.com/noixdev/pulsix/internal/agent/probes"
)

// redactLoop builds a loop whose agent.yaml holds secretValue and has
// been read.
func redactLoop(t *testing.T) (*loopTest, *Loop) {
	t.Helper()
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	l := lt.newLoop()
	l.lookAndApplyLocal()
	return lt, l
}

func leaky(prefix string) string { return prefix + " " + secretValue + " tail" }

func assertRedactedOnWire(t *testing.T, stream *fakeStream) {
	t.Helper()
	assertNothingSent(t, stream, "")
	found := false
	for _, m := range stream.sentMessages() {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		found = found || bytes.Contains(b, []byte("[secret]"))
	}
	if !found {
		t.Fatal("no [secret] placeholder was sent")
	}
}

func TestLoopRedactsCheckResultsBeforeSending(t *testing.T) {
	_, l := redactLoop(t)
	stream := newFakeStream()
	l.sender = newStreamSender(stream)
	label := leaky("label")
	in := []checks.Result{
		{CheckKey: "db", Item: leaky("item"), Metric: "db.state", Label: &label},
		{CheckKey: "db", Item: "x", Metric: "db.up", Err: leaky("dial")},
	}
	l.handleResults(in)
	assertRedactedOnWire(t, stream)
	if *in[0].Label != label || in[1].Err != leaky("dial") {
		t.Fatal("redaction wrote into the scheduler's slice")
	}
	got := checkResultMessages(stream)[0].GetResults()
	if got[0].GetItem() != "item [secret] tail" || got[0].GetLabel() != "label [secret] tail" || got[1].GetError() != "dial [secret] tail" {
		t.Fatalf("sent = %v", got)
	}
}

func TestLoopRedactsBufferedResults(t *testing.T) {
	lt, l := redactLoop(t)
	l.handleResults([]checks.Result{{CheckKey: "db", Metric: "db.up", Err: leaky("dial"), At: lt.now()}})
	buffered := l.buffer.Drain()
	if len(buffered) != 1 || buffered[0].Err != "dial [secret] tail" {
		t.Fatalf("buffered = %+v", buffered)
	}
}

func TestLoopRedactsProbeResultsBeforeSending(t *testing.T) {
	_, l := redactLoop(t)
	stream := newFakeStream()
	l.sender = newStreamSender(stream)
	label := leaky("label")
	l.handleProbeResults([]probes.Result{
		{TargetHostID: uuid.New(), CheckKey: "http", Item: leaky("item"), Metric: "http.status", Label: &label},
		{TargetHostID: uuid.New(), CheckKey: "http", Metric: "http.up", Err: leaky("refused")},
	})
	assertRedactedOnWire(t, stream)
}

func TestLoopRedactsFactsBeforeSending(t *testing.T) {
	lt, l := redactLoop(t)
	lt.facts.(*fakeCollector).facts = map[string]string{"os": "linux", "motd": leaky("motd")}
	stream := newFakeStream()
	if err := l.pushFacts(context.Background(), newStreamSender(stream)); err != nil {
		t.Fatal(err)
	}
	assertRedactedOnWire(t, stream)
	if got := stream.sentMessages()[0].GetFacts().GetValues(); got["os"] != "linux" || got["motd"] != "motd [secret] tail" {
		t.Fatalf("facts = %v", got)
	}
}

// TestLoopLeavesResultsAloneWithoutSecrets: no file, nothing replaced.
func TestLoopLeavesResultsAloneWithoutSecrets(t *testing.T) {
	lt := newLoopTest(t)
	l := lt.newLoop()
	l.lookAndApplyLocal()
	l.handleResults([]checks.Result{{CheckKey: "db", Metric: "db.up", Err: leaky("dial"), At: lt.now()}})
	if got := l.buffer.Drain(); got[0].Err != leaky("dial") {
		t.Fatalf("buffered = %+v", got)
	}
}

// TestLoopRedactsTheLogRing: a log line carrying a secret value is stored
// in the status page's ring with the value replaced.
func TestLoopRedactsTheLogRing(t *testing.T) {
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	ring := logring.New(10)
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), WithLogRing(ring))...)
	l.lookAndApplyLocal()
	_, _ = ring.Write([]byte(leaky("line") + "\n"))
	if got := ring.Lines(); got[len(got)-1] != "line [secret] tail" {
		t.Fatalf("ring = %q", got)
	}
}

// TestLoopRedactsQuotedValuesInTheLogRing: slog's text handler writes a
// value holding a quote or a backslash escaped; the ring replaces that
// form too (review 3.132, finding 1).
func TestLoopRedactsQuotedValuesInTheLogRing(t *testing.T) {
	const tricky = `Tr0ub"dor\3`
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, "secrets:\n  db:\n    password: '"+tricky+"'\n")
	ring := logring.New(10)
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), WithLogRing(ring))...)
	l.lookAndApplyLocal()
	logger := slog.New(slog.NewTextHandler(ring, nil))
	logger.Warn("advisory", "err", "auth failed for "+tricky)
	got := strings.Join(ring.Lines(), "\n")
	if strings.Contains(got, "Tr0ub") || !strings.Contains(got, "[secret]") {
		t.Fatalf("ring = %q", got)
	}
}

// TestLoopDropsResultsRedactionPushesOutOfBounds: a 4-byte value becomes
// the 8-byte placeholder, which can push an item or a label past the
// manager's 512 bytes; such a result is dropped (and logged), never sent,
// so the manager does not end the stream (review 3.132, finding 3). An
// error is cut back to its bound instead.
func TestLoopDropsResultsRedactionPushesOutOfBounds(t *testing.T) {
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, "secrets:\n  db:\n    password: abcd\n")
	l := lt.newLoop()
	l.lookAndApplyLocal()
	stream := newFakeStream()
	l.sender = newStreamSender(stream)
	longItem := "/" + strings.Repeat("m", 505) + "abcd" // 510 bytes, 514 once redacted
	longLabel := strings.Repeat("l", 508) + "abcd"
	longErr := strings.Repeat("e", 2040) + "abcd" // 2044 bytes, 2048+ once redacted
	l.handleResults([]checks.Result{
		{CheckKey: "disk", Item: longItem, Metric: "disk.used_pct", Value: floatPtr(1), At: lt.now()},
		{CheckKey: "svc", Item: "x", Metric: "svc.state", Label: &longLabel, At: lt.now()},
		{CheckKey: "disk", Item: "/", Metric: "disk.used_pct", Err: longErr, At: lt.now()},
		{CheckKey: "disk", Item: "/ok", Metric: "disk.used_pct", Value: floatPtr(2), At: lt.now()},
	})
	got := checkResultMessages(stream)[0].GetResults()
	if len(got) != 2 || got[0].GetItem() != "/" || got[1].GetItem() != "/ok" {
		t.Fatalf("sent = %v", got)
	}
	if e := got[0].GetError(); len(e) > 2048 || strings.Contains(e, "abcd") {
		t.Fatalf("error = %d bytes", len(e))
	}
	if !strings.Contains(lt.logText(), "too long once secret values were replaced") {
		t.Fatalf("no warning: %s", lt.logText())
	}

	pl := strings.Repeat("p", 508) + "abcd"
	l.handleProbeResults([]probes.Result{
		{TargetHostID: uuid.New(), CheckKey: "web", Metric: "http.text", Label: &pl, At: lt.now()},
		{TargetHostID: uuid.New(), CheckKey: "web", Metric: "http.up", Value: floatPtr(1), At: lt.now()},
	})
	var probeMsgs int
	for _, m := range stream.sentMessages() {
		if pr := m.GetProbeResults(); pr != nil {
			probeMsgs++
			if len(pr.GetResults()) != 1 || pr.GetResults()[0].GetMetric() != "http.up" {
				t.Fatalf("probe results = %v", pr.GetResults())
			}
		}
	}
	if probeMsgs != 1 {
		t.Fatalf("probe messages = %d", probeMsgs)
	}
}

// TestLoopCutsRedactedFactsBackToTheirBound: a fact value stays within 512
// bytes once redacted, and a value cut at the bound does not keep the
// start of a secret.
func TestLoopCutsRedactedFactsBackToTheirBound(t *testing.T) {
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, "secrets:\n  db:\n    password: abcd\n    token: zyxwvuts\n")
	lt.facts.(*fakeCollector).facts = map[string]string{
		"grown": strings.Repeat("g", 500) + "abcd" + "abcd",
		"cut":   strings.Repeat("c", 507) + "zyxwv", // 512: the collector cut "zyxwvuts" here
	}
	l := lt.newLoop()
	l.lookAndApplyLocal()
	stream := newFakeStream()
	if err := l.pushFacts(context.Background(), newStreamSender(stream)); err != nil {
		t.Fatal(err)
	}
	got := stream.sentMessages()[0].GetFacts().GetValues()
	if len(got["grown"]) > 512 || strings.Contains(got["grown"], "abcd") {
		t.Fatalf("grown = %d bytes", len(got["grown"]))
	}
	if got["cut"] != strings.Repeat("c", 507) {
		t.Fatalf("cut = %q", got["cut"][500:])
	}
}

// TestLoopStripsTheCutStartOfASecretFromAnError: an error text cut at its
// bound inside a secret value loses the value's start too (review 3.132,
// finding 8).
func TestLoopStripsTheCutStartOfASecretFromAnError(t *testing.T) {
	lt := newLoopTest(t)
	writeSecretsFile(t, lt.cfg.SecretsPath, "secrets:\n  db:\n    password: zyxwvuts\n")
	l := lt.newLoop()
	l.lookAndApplyLocal()
	cut := strings.Repeat("e", 2043) + "zyxwv" // 2048: cut inside the value
	l.handleResults([]checks.Result{{CheckKey: "db", Metric: "db.up", Err: cut, At: lt.now()}})
	if got := l.buffer.Drain()[0].Err; got != strings.Repeat("e", 2043) {
		t.Fatalf("err tail = %q", got[2030:])
	}
}
