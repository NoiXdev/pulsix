package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

// registerAgentWithLocalChecks enrolls an agent for hostID and sends one
// heartbeat reporting secretNames and the given local checks.
func (f checksFixture) registerAgentWithLocalChecks(t *testing.T, hostID uuid.UUID, serial string, secretNames []string, local []agents.LocalCheckInput) {
	t.Helper()
	ctx := db.WithOrgScope(context.Background(), f.orgID)
	now := time.Now()
	if err := f.s.Deps.Agents.Register(ctx, agents.Registration{
		OrgID: f.orgID, HostID: hostID, AgentName: serial, Version: "v1", Serial: serial,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Agents.Heartbeat(context.Background(), serial, agents.HeartbeatReport{
		Version: "v1", SecretNames: secretNames, SecretsState: "ok", LocalChecks: local,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestHostChecksListLocalChecks (backlog 3.5a): the checks defined in the
// host's agent.yaml follow the effective definitions, in key order, each
// read-only — source and origin "local", no definition_id, enabled — with
// its values (only the items it still asks for) and the missing-secret
// warning like any check. An unmonitored host lists none: it resolves to
// an empty set, local checks included.
func TestHostChecksListLocalChecks(t *testing.T) {
	f := newChecksFixture(t, "host-checks-local")
	f.registerAgentWithLocalChecks(t, f.web, "serial-host-local", []string{"other"}, []agents.LocalCheckInput{
		{Key: "local-pg", Plugin: "postgres", ParamsJSON: `{"secret":"db-main"}`, IntervalSeconds: 300},
		{Key: "local-disk", Plugin: "disk", ParamsJSON: `{"mounts":["/"]}`, IntervalSeconds: 60},
	})
	f.registerAgentWithLocalChecks(t, f.cold, "serial-cold-local", nil, []agents.LocalCheckInput{
		{Key: "local-disk", Plugin: "disk", ParamsJSON: `{}`, IntervalSeconds: 60},
	})
	at := time.Now().Add(-10 * time.Second)
	f.record(t,
		checks.Observation{HostID: f.web, CheckKey: "local-disk", Item: "/", Metric: "disk.used_pct", At: at, Value: floatPtr(42)},
		checks.Observation{HostID: f.web, CheckKey: "local-disk", Item: "/data", Metric: "disk.used_pct", At: at, Value: floatPtr(7)},
	)

	resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("host checks = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var raw struct {
		Checks []map[string]json.RawMessage `json:"checks"`
	}
	decodeInto(t, resp, &raw)
	keys := make([]string, len(raw.Checks))
	for i, c := range raw.Checks {
		if err := json.Unmarshal(c["key"], &keys[i]); err != nil {
			t.Fatal(err)
		}
	}
	n := len(keys)
	if n < 3 || keys[n-2] != "local-disk" || keys[n-1] != "local-pg" {
		t.Fatalf("keys = %v, want the effective definitions, then local-disk and local-pg", keys)
	}
	if slices.Contains(keys[:n-2], "local-disk") || slices.Contains(keys[:n-2], "local-pg") {
		t.Fatalf("keys = %v, want each local check once, after the definitions", keys)
	}
	for _, c := range raw.Checks[n-2:] {
		for _, absent := range []string{"definition_id", "source_id", "source_name"} {
			if v, ok := c[absent]; ok {
				t.Fatalf("local check %s carries %s = %s, want it absent", c["key"], absent, v)
			}
		}
	}
	for _, c := range raw.Checks[:n-2] {
		if _, ok := c["definition_id"]; !ok {
			t.Fatalf("definition %s lacks definition_id", c["key"])
		}
	}

	var body struct {
		Checks []struct {
			Key             string           `json:"key"`
			Plugin          string           `json:"plugin"`
			Params          map[string]any   `json:"params"`
			IntervalSeconds int              `json:"interval_seconds"`
			Enabled         bool             `json:"enabled"`
			Origin          string           `json:"origin"`
			Source          string           `json:"source"`
			MissingSecret   string           `json:"missing_secret"`
			Values          []hostCheckValue `json:"values"`
		} `json:"checks"`
	}
	resp = requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.web)+"/checks", nil)
	decodeInto(t, resp, &body)
	disk, pg := body.Checks[n-2], body.Checks[n-1]
	for _, c := range []struct {
		key, plugin, origin, source string
		interval                    int
		enabled                     bool
	}{
		{disk.Key, disk.Plugin, disk.Origin, disk.Source, disk.IntervalSeconds, disk.Enabled},
		{pg.Key, pg.Plugin, pg.Origin, pg.Source, pg.IntervalSeconds, pg.Enabled},
	} {
		if c.origin != "local" || c.source != "local" || !c.enabled {
			t.Fatalf("%s: origin %q, source %q, enabled %v; want local, local, true", c.key, c.origin, c.source, c.enabled)
		}
	}
	if disk.Plugin != "disk" || disk.IntervalSeconds != 60 || pg.Plugin != "postgres" || pg.IntervalSeconds != 300 || pg.Params["secret"] != "db-main" {
		t.Fatalf("local rows = %+v / %+v, want disk every 60 s and postgres every 300 s with its params", disk, pg)
	}
	if len(disk.Values) != 1 || disk.Values[0].Item != "/" || disk.Values[0].Value == nil || *disk.Values[0].Value != 42 {
		t.Fatalf("local-disk values = %+v, want only the asked-for mount /", disk.Values)
	}
	if pg.MissingSecret != "db-main" || disk.MissingSecret != "" {
		t.Fatalf("missing_secret = %q (pg) / %q (disk), want db-main for the postgres check only", pg.MissingSecret, disk.MissingSecret)
	}

	cold := f.hostChecks(t, f.viewerC, f.cold)
	for _, c := range cold.Checks {
		if c.Source == "local" {
			t.Fatalf("unmonitored host lists the local check %+v", c)
		}
	}
}
