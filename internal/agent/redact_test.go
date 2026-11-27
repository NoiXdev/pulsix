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
