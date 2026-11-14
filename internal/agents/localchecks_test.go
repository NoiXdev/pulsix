package agents_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
)

// paramsMarker rides in every params value the tests send, so a log line
// that leaked params would carry it.
const paramsMarker = "PARAMS-MARKER"

// heartbeatLocal sends one heartbeat through a service with a captured
// logger and returns the local checks report the store received and the
// log text.
func heartbeatLocal(t *testing.T, r agents.HeartbeatReport) (agents.LocalChecksReport, string) {
	t.Helper()
	var buf bytes.Buffer
	st := &fakeStore{}
	svc := agents.NewService(st, agents.WithProvisioner(&fakeProvisioner{}),
		agents.WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-l")); err != nil {
		t.Fatal(err)
	}
	if r.Version == "" {
		r.Version = "v2"
	}
	if err := svc.Heartbeat(ctx, "serial-l", r); err != nil {
		t.Fatal(err)
	}
	got, err := svc.LocalChecksByHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), paramsMarker) {
		t.Fatalf("a log line names params:\n%s", buf.String())
	}
	return got, buf.String()
}

// diskCheck is a valid local check whose params carry paramsMarker.
func diskCheck(key string) agents.LocalCheckInput {
	return agents.LocalCheckInput{Key: key, Plugin: "disk", ParamsJSON: `{"mounts":["/` + paramsMarker + `"]}`, IntervalSeconds: 60}
}

func keysOf(checks []agents.LocalCheck) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Key
	}
	return out
}

func TestHeartbeatStoresValidLocalChecks(t *testing.T) {
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		diskCheck("local-z"),
		{Key: "local-cpu", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 10},
		{Key: "local-pg", Plugin: "postgres", ParamsJSON: `{"secret":"pg_main","databases":["app"]}`, IntervalSeconds: 86400},
	}, LocalCheckProblems: []string{`line 7: check "x": unknown plugin`}})
	if logs != "" {
		t.Fatalf("valid checks logged:\n%s", logs)
	}
	if !got.Known || !slices.Equal(keysOf(got.Checks), []string{"local-cpu", "local-pg", "local-z"}) {
		t.Fatalf("report = %+v, want known with the three checks in key order", got)
	}
	pg := got.Checks[1]
	if pg.Plugin != "postgres" || pg.Interval != 24*time.Hour || pg.Params["secret"] != "pg_main" ||
		fmt.Sprint(pg.Params["databases"]) != "[app]" {
		t.Fatalf("postgres check = %+v", pg)
	}
	if got.Checks[0].Params == nil || len(got.Checks[0].Params) != 0 || got.Checks[0].Interval != 10*time.Second {
		t.Fatalf("cpu check = %+v, want empty params and 10 s", got.Checks[0])
	}
	if !slices.Equal(got.Problems, []string{`line 7: check "x": unknown plugin`}) {
		t.Fatalf("problems = %q", got.Problems)
	}
}

// Each refusal drops only its own check and logs its key and a fixed
// reason — never the params.
func TestHeartbeatDropsInvalidLocalChecks(t *testing.T) {
	cases := []struct {
		name   string
		bad    agents.LocalCheckInput
		logKey string
		reason string
	}{
		{"key without the prefix", agents.LocalCheckInput{Key: "backup", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60}, "backup", "key"},
		{"bare prefix", agents.LocalCheckInput{Key: "local-", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60}, "local-", "key"},
		{"key with upper case", agents.LocalCheckInput{Key: "local-Backup", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60}, "local-Backup", "key"},
		{"unknown plugin", agents.LocalCheckInput{Key: "local-b", Plugin: "nope", ParamsJSON: "{}", IntervalSeconds: 60}, "local-b", "unknown plugin"},
		{"server-run plugin", agents.LocalCheckInput{Key: "local-b", Plugin: "icmp", ParamsJSON: `{"target":"` + paramsMarker + `"}`, IntervalSeconds: 60}, "local-b", "server"},
		{"interval 5 s", agents.LocalCheckInput{Key: "local-b", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 5}, "local-b", "interval"},
		{"interval 90000 s", agents.LocalCheckInput{Key: "local-b", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 90000}, "local-b", "interval"},
		{"params an array", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `["` + paramsMarker + `"]`, IntervalSeconds: 60}, "local-b", "object"},
		{"params null", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `null`, IntervalSeconds: 60}, "local-b", "object"},
		{"params not JSON", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `{"mounts":` + paramsMarker, IntervalSeconds: 60}, "local-b", "object"},
		{"params with trailing data", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `{} {"` + paramsMarker + `":1}`, IntervalSeconds: 60}, "local-b", "object"},
		{"params over 2048 bytes", agents.LocalCheckInput{Key: "local-b", Plugin: "disk",
			ParamsJSON: `{"mounts":["/` + paramsMarker + strings.Repeat("a", 2048) + `"]}`, IntervalSeconds: 60}, "local-b", "2048"},
		{"params Validate refuses", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `{"mounts":"` + paramsMarker + `"}`, IntervalSeconds: 60}, "local-b", "params"},
		{"params with an unknown key", agents.LocalCheckInput{Key: "local-b", Plugin: "disk", ParamsJSON: `{"` + paramsMarker + `":1}`, IntervalSeconds: 60}, "local-b", "params"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
				diskCheck("local-a"), tc.bad, diskCheck("local-c"),
			}})
			if !got.Known || !slices.Equal(keysOf(got.Checks), []string{"local-a", "local-c"}) {
				t.Fatalf("kept %q, want local-a and local-c", keysOf(got.Checks))
			}
			if lines := strings.Count(strings.TrimSpace(logs), "\n") + 1; logs == "" || lines != 1 {
				t.Fatalf("log = %q, want one line", logs)
			}
			if !strings.Contains(logs, "key="+tc.logKey+" ") || !strings.Contains(logs, tc.reason) {
				t.Fatalf("log = %q, want key %q and a reason naming %q", logs, tc.logKey, tc.reason)
			}
		})
	}
}

func TestHeartbeatDropsTheSecondOfADuplicateKey(t *testing.T) {
	first := diskCheck("local-a")
	second := agents.LocalCheckInput{Key: "local-a", Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{first, second}})
	if len(got.Checks) != 1 || got.Checks[0].Plugin != "disk" {
		t.Fatalf("kept %+v, want only the first (disk)", got.Checks)
	}
	if !strings.Contains(logs, "key=local-a") || !strings.Contains(logs, "duplicate") {
		t.Fatalf("log = %q, want the duplicate named", logs)
	}
}

func TestHeartbeatKeepsAtMost32LocalChecks(t *testing.T) {
	var in []agents.LocalCheckInput
	for i := range agents.MaxLocalChecks + 1 {
		in = append(in, diskCheck(fmt.Sprintf("local-%02d", agents.MaxLocalChecks-i)))
	}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	if agents.MaxLocalChecks != 32 || len(got.Checks) != 32 {
		t.Fatalf("kept %d, want 32", len(got.Checks))
	}
	if got.Checks[0].Key != "local-00" || got.Checks[31].Key != "local-31" {
		t.Fatalf("kept %q…%q, want the first 32 in key order", got.Checks[0].Key, got.Checks[31].Key)
	}
	if !strings.Contains(logs, "key=local-32") || !strings.Contains(logs, "more than 32") {
		t.Fatalf("log = %q, want the 33rd named", logs)
	}
}

// A key that is not printable or too long is logged cut and printable.
func TestHeartbeatLogsAJunkKeyBounded(t *testing.T) {
	junk := "x\x00\u202e" + strings.Repeat("k", 500)
	_, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: junk, Plugin: "cpu", ParamsJSON: "{}", IntervalSeconds: 60},
	}})
	if logs == "" || strings.Contains(logs, "\x00") || strings.Contains(logs, "\u202e") || strings.Contains(logs, strings.Repeat("k", 100)) {
		t.Fatalf("log = %q, want the key printable and cut", logs)
	}
}

func TestHeartbeatBoundsLocalCheckProblems(t *testing.T) {
	var problems []string
	for i := range 40 {
		problems = append(problems, fmt.Sprintf("line %d: refused", i))
	}
	problems[0] = strings.Repeat("€", 100) // 300 bytes
	problems[1] = "bad \xff\n line"
	got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalCheckProblems: problems})
	if agents.MaxLocalCheckProblems != 32 || len(got.Problems) != 32 || got.Problems[31] != "line 31: refused" {
		t.Fatalf("problems = %d (last %q), want the first 32", len(got.Problems), got.Problems[len(got.Problems)-1])
	}
	if got.Problems[0] != strings.Repeat("€", 85) || !utf8.ValidString(got.Problems[0]) {
		t.Fatalf("long problem = %d bytes, want 255 (85 whole runes)", len(got.Problems[0]))
	}
	if got.Problems[1] != "bad �  line" {
		t.Fatalf("problem = %q, want valid UTF-8 on one line", got.Problems[1])
	}
	if got.Checks == nil || len(got.Checks) != 0 {
		t.Fatalf("checks = %#v, want known and empty", got.Checks)
	}
}

// An agent that sends no secrets state is older than agent.yaml: its
// local checks are unknown, whatever rides along.
func TestHeartbeatOldAgentLocalChecksUnknown(t *testing.T) {
	got, _ := heartbeatLocal(t, agents.HeartbeatReport{
		LocalChecks: []agents.LocalCheckInput{diskCheck("local-a")}, LocalCheckProblems: []string{"x"},
	})
	if got.Known || got.Checks != nil || got.Problems != nil {
		t.Fatalf("report = %+v, want unknown", got)
	}
}

func TestHeartbeatZeroLocalChecksIsKnown(t *testing.T) {
	got, _ := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "absent"})
	if !got.Known || got.Checks == nil || len(got.Checks) != 0 || got.Problems == nil || len(got.Problems) != 0 {
		t.Fatalf("report = %#v, want known with empty, non-nil lists", got)
	}
}

// The host page's reads: a host without an agent is unknown, not an
// error; LocalChecksByHosts answers for every listed host that runs one.
func TestLocalChecksByHosts(t *testing.T) {
	svc, _, _, _ := newService(t)
	org := uuid.Must(uuid.NewV7())
	a, b, none := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if got, err := svc.LocalChecksByHost(ctx, org, none); err != nil || got.Known {
		t.Fatalf("no agent: %+v, %v; want unknown, nil", got, err)
	}
	for i, h := range []uuid.UUID{a, b} {
		serial := fmt.Sprintf("serial-%d", i)
		if err := svc.Register(ctx, reg(org, h, fmt.Sprintf("agent-%d", i), "v1", serial)); err != nil {
			t.Fatal(err)
		}
		if err := svc.Heartbeat(ctx, serial, agents.HeartbeatReport{Version: "v1", SecretsState: "ok",
			LocalChecks: []agents.LocalCheckInput{diskCheck(fmt.Sprintf("local-%d", i))}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.LocalChecksByHosts(ctx, org, []uuid.UUID{a, b, none})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(keysOf(got[a].Checks), []string{"local-0"}) ||
		!slices.Equal(keysOf(got[b].Checks), []string{"local-1"}) || got[none].Known {
		t.Fatalf("by hosts = %+v", got)
	}
}
