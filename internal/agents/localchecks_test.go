package agents_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checkplugins"
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
	for i := range checkplugins.MaxLocalChecks + 1 {
		in = append(in, diskCheck(fmt.Sprintf("local-%02d", checkplugins.MaxLocalChecks-i)))
	}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	if checkplugins.MaxLocalChecks != 32 || len(got.Checks) != 32 {
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
	if checkplugins.MaxLocalCheckProblems != 32 || len(got.Problems) != 32 || got.Problems[31] != "line 31: refused" {
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

// noHostAgentStore refuses the whole-row agent read, so a test can pin
// that a read does not go through it.
type noHostAgentStore struct{ *fakeStore }

func (noHostAgentStore) GetByHost(context.Context, uuid.UUID, uuid.UUID) (agents.Agent, error) {
	return agents.Agent{}, errors.New("GetByHost must not be read")
}

// LocalChecksByHost is on the rule evaluation's path (every judged host,
// every pass), so it reads the narrow local checks query for its one host,
// not the whole agent row with its joins.
func TestLocalChecksByHostReadsOnlyTheLocalChecks(t *testing.T) {
	st := &fakeStore{}
	svc := agents.NewService(noHostAgentStore{st}, agents.WithProvisioner(&fakeProvisioner{}))
	org, host, none := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Register(ctx, reg(org, host, "agent-a", "v1", "serial-narrow")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(ctx, "serial-narrow", agents.HeartbeatReport{Version: "v1", SecretsState: "ok",
		LocalChecks: []agents.LocalCheckInput{diskCheck("local-a")}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.LocalChecksByHost(ctx, org, host)
	if err != nil || !got.Known || !slices.Equal(keysOf(got.Checks), []string{"local-a"}) {
		t.Fatalf("LocalChecksByHost = %+v, %v; want local-a", got, err)
	}
	if got, err := svc.LocalChecksByHost(ctx, org, none); err != nil || got.Known {
		t.Fatalf("no agent: %+v, %v; want unknown, nil", got, err)
	}
}

// A refused check does not count toward the cap: one that sorts first and
// is dropped leaves room for 32 valid ones.
func TestHeartbeatRefusedLocalCheckLeavesRoomUnderTheCap(t *testing.T) {
	in := []agents.LocalCheckInput{{Key: "local-0", Plugin: "nope", ParamsJSON: "{}", IntervalSeconds: 60}}
	for i := range checkplugins.MaxLocalChecks {
		in = append(in, diskCheck(fmt.Sprintf("local-%02d", i+10)))
	}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	if len(got.Checks) != checkplugins.MaxLocalChecks || got.Checks[0].Key != "local-10" {
		t.Fatalf("kept %d (first %q), want all 32 valid ones", len(got.Checks), got.Checks[0].Key)
	}
	if !strings.Contains(logs, "key=local-0 ") || strings.Contains(logs, "more than 32") {
		t.Fatalf("log = %q, want only the refused check", logs)
	}
}

// Params within 2048 bytes as sent but over it once canonical (json.Marshal
// escapes '<' as six bytes) are refused, as the agent refuses them.
func TestHeartbeatDropsLocalCheckOverTheCanonicalSize(t *testing.T) {
	params := `{"mounts":["/` + paramsMarker + strings.Repeat("<", 400) + `"]}`
	if len(params) > checkplugins.MaxLocalCheckParamsBytes {
		t.Fatalf("test params are %d bytes as sent; want them within the cap", len(params))
	}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: []agents.LocalCheckInput{
		{Key: "local-esc", Plugin: "disk", ParamsJSON: params, IntervalSeconds: 60}, diskCheck("local-ok"),
	}})
	if !slices.Equal(keysOf(got.Checks), []string{"local-ok"}) {
		t.Fatalf("kept %q, want only local-ok", keysOf(got.Checks))
	}
	if !strings.Contains(logs, "key=local-esc ") || !strings.Contains(logs, "2048") {
		t.Fatalf("log = %q, want local-esc dropped for its size", logs)
	}
}

// logLines splits captured log text into its lines.
func logLines(logs string) []string {
	if logs == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(logs, "\n"), "\n")
}

// One heartbeat logs at most 32 dropped checks one by one; the rest are
// counted in one more line.
func TestHeartbeatCapsTheDropLogLines(t *testing.T) {
	var in []agents.LocalCheckInput
	for i := range 40 {
		in = append(in, agents.LocalCheckInput{Key: fmt.Sprintf("local-%02d", i), Plugin: "nope", ParamsJSON: "{}", IntervalSeconds: 60})
	}
	_, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	lines := logLines(logs)
	if len(lines) != 33 {
		t.Fatalf("logged %d lines, want 32 plus one count line:\n%s", len(lines), logs)
	}
	if !strings.Contains(lines[31], "key=local-31 ") || !strings.Contains(lines[32], "count=8") || strings.Contains(lines[32], "key=") {
		t.Fatalf("last lines = %q / %q, want local-31 then a count of 8", lines[31], lines[32])
	}
}

// Only the first 64 entries as sent are examined at all: an entry after
// them is neither kept nor validated, only counted in the summary line.
func TestHeartbeatExaminesAtMost64LocalChecks(t *testing.T) {
	var in []agents.LocalCheckInput
	for i := range 64 {
		in = append(in, agents.LocalCheckInput{Key: fmt.Sprintf("local-b%02d", i), Plugin: "nope", ParamsJSON: "{}", IntervalSeconds: 60})
	}
	for i := range 36 {
		in = append(in, diskCheck(fmt.Sprintf("local-a%02d", i)))
	}
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	if !got.Known || len(got.Checks) != 0 {
		t.Fatalf("kept %q, want nothing: the valid entries come after the first 64", keysOf(got.Checks))
	}
	lines := logLines(logs)
	if len(lines) != 33 || !strings.Contains(lines[32], "count=68") {
		t.Fatalf("logged %d lines (last %q), want 32 plus a count of 68 (32 more examined, 36 not examined)", len(lines), lines[len(lines)-1])
	}
}

// Past the cap nothing more is parsed: the 33rd check is dropped for the
// cap even when its params would also be refused.
func TestHeartbeatPastTheCapDropsWithoutParsing(t *testing.T) {
	var in []agents.LocalCheckInput
	for i := range checkplugins.MaxLocalChecks {
		in = append(in, diskCheck(fmt.Sprintf("local-%02d", i)))
	}
	in = append(in, agents.LocalCheckInput{Key: "local-zz", Plugin: "disk", ParamsJSON: "not json", IntervalSeconds: 60})
	got, logs := heartbeatLocal(t, agents.HeartbeatReport{SecretsState: "ok", LocalChecks: in})
	if len(got.Checks) != checkplugins.MaxLocalChecks || !strings.Contains(logs, "key=local-zz ") || !strings.Contains(logs, "more than 32") {
		t.Fatalf("kept %d, log = %q; want local-zz dropped for the cap", len(got.Checks), logs)
	}
}
