package agents_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

func unknownPlugin(key string) agents.LocalCheckInput {
	return agents.LocalCheckInput{Key: key, Plugin: "nope", ParamsJSON: `{"x":"` + paramsMarker + `"}`, IntervalSeconds: 60}
}

// The dropped set is logged once per change per agent: the same set over
// several heartbeats is one warning, a changed set logs again, and an empty
// set forgets the agent so the same set later logs again.
func TestHeartbeatLogsADroppedSetOncePerChange(t *testing.T) {
	var buf bytes.Buffer
	svc := agents.NewService(&fakeStore{}, agents.WithProvisioner(&fakeProvisioner{}),
		agents.WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	for _, serial := range []string{"serial-a", "serial-b"} {
		if err := svc.Register(ctx, reg(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "agent-"+serial, "v1", serial)); err != nil {
			t.Fatal(err)
		}
	}
	beat := func(serial string, in ...agents.LocalCheckInput) int {
		t.Helper()
		buf.Reset()
		if err := svc.Heartbeat(ctx, serial, agents.HeartbeatReport{Version: "v2", SecretsState: "ok", LocalChecks: in}); err != nil {
			t.Fatal(err)
		}
		return len(logLines(buf.String()))
	}
	a, b := unknownPlugin("local-a"), unknownPlugin("local-b")
	steps := []struct {
		name   string
		serial string
		in     []agents.LocalCheckInput
		want   int
	}{
		{"first sight logs", "serial-a", []agents.LocalCheckInput{a}, 1},
		{"same set is silent", "serial-a", []agents.LocalCheckInput{a}, 0},
		{"same set again is silent", "serial-a", []agents.LocalCheckInput{a}, 0},
		{"another agent logs on its own", "serial-b", []agents.LocalCheckInput{a}, 1},
		{"a grown set logs again", "serial-a", []agents.LocalCheckInput{a, b}, 2},
		{"a shrunk set logs again", "serial-a", []agents.LocalCheckInput{b}, 1},
		{"an empty set is silent", "serial-a", nil, 0},
		{"the set after an empty one logs again", "serial-a", []agents.LocalCheckInput{b}, 1},
		{"the other agent is still deduped", "serial-b", []agents.LocalCheckInput{a}, 0},
	}
	for _, s := range steps {
		if got := beat(s.serial, s.in...); got != s.want {
			t.Fatalf("%s: %d log lines, want %d", s.name, got, s.want)
		}
	}
	// the same key refused for another reason is a changed set
	if got := beat("serial-b", agents.LocalCheckInput{Key: "local-a", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 5}); got != 1 {
		t.Fatalf("a new reason: %d log lines, want 1", got)
	}
}

func TestHeartbeatAppendsDroppedChecksToTheProblems(t *testing.T) {
	got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok",
		LocalChecks:        []agents.LocalCheckInput{diskCheck("local-ok"), unknownPlugin("local-new"), {Key: "local-x", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 5}},
		LocalCheckProblems: []string{"line 7: refused"}})
	want := []string{
		"line 7: refused",
		"local-new: ignored by the server (an unknown plugin)",
		"local-x: ignored by the server (the interval is out of bounds)",
	}
	if fmt.Sprint(got.Problems) != fmt.Sprint(want) {
		t.Fatalf("problems = %q, want %q", got.Problems, want)
	}
	if strings.Contains(strings.Join(got.Problems, "\n"), paramsMarker) {
		t.Fatalf("problems name params: %q", got.Problems)
	}
}

func TestHeartbeatDroppedProblemsKeepToTheCap(t *testing.T) {
	var in []agents.LocalCheckInput
	for i := range 40 {
		in = append(in, unknownPlugin(fmt.Sprintf("local-%02d", i)))
	}
	t.Run("alone: the first 32 keys in order", func(t *testing.T) {
		got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
		if len(got.Problems) != checkplugins.MaxLocalCheckProblems || !strings.HasPrefix(got.Problems[0], "local-00: ") ||
			!strings.HasPrefix(got.Problems[31], "local-31: ") {
			t.Fatalf("problems = %q", got.Problems)
		}
	})
	t.Run("the agent's own problems come first", func(t *testing.T) {
		own := []string{"own 0", "own 1", "own 2"}
		got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in, LocalCheckProblems: own})
		if len(got.Problems) != 32 || got.Problems[2] != "own 2" || !strings.HasPrefix(got.Problems[3], "local-00: ") ||
			!strings.HasPrefix(got.Problems[31], "local-28: ") {
			t.Fatalf("problems = %q", got.Problems)
		}
	})
	t.Run("a full list of the agent's own leaves no room", func(t *testing.T) {
		var own []string
		for i := range 32 {
			own = append(own, fmt.Sprintf("own %d", i))
		}
		got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in, LocalCheckProblems: own})
		if len(got.Problems) != 32 || got.Problems[31] != "own 31" {
			t.Fatalf("problems = %q", got.Problems)
		}
	})
}

func TestHeartbeatDroppedProblemKeyIsBounded(t *testing.T) {
	junk := "x\x00\u202e" + strings.Repeat("k", 500)
	got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: junk, Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60},
	}})
	if len(got.Problems) != 1 || len(got.Problems[0]) > agents.MaxLocalCheckProblemBytes ||
		strings.ContainsAny(got.Problems[0], "\x00\u202e") || strings.Contains(got.Problems[0], strings.Repeat("k", 100)) {
		t.Fatalf("problems = %q, want one printable bounded line", got.Problems)
	}
}
