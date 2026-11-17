package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func TestOpsToolsListedByArea(t *testing.T) {
	schedule := []string{"list_schedules", "get_schedule", "list_schedule_runs", "get_calendar"}
	cases := []struct {
		name  string
		areas []tenancy.Area
		want  []string
		not   []string
	}{
		{"templates.read", []tenancy.Area{tenancy.AreaTemplatesRead}, schedule, []string{"list_activity", "set_host_tags", "start_connection_test", "get_connection_test"}},
		{"activity.read", []tenancy.Area{tenancy.AreaActivityRead}, []string{"list_activity"}, append(append([]string{}, schedule...), "set_host_tags", "start_connection_test")},
		{"inventory.write", []tenancy.Area{tenancy.AreaInventoryWrite}, []string{"set_host_tags", "start_connection_test", "get_connection_test"}, append(append([]string{}, schedule...), "list_activity")},
		{"inventory.read", []tenancy.Area{tenancy.AreaInventoryRead}, []string{"get_connection_test"}, []string{"set_host_tags", "start_connection_test", "list_activity"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgFixture(t, tc.areas...)
			names := toolNames(t, mustConnect(t, f.s, f.token))
			for _, n := range tc.want {
				if !names[n] {
					t.Errorf("%s is not listed", n)
				}
			}
			for _, n := range tc.not {
				if names[n] {
					t.Errorf("%s is listed", n)
				}
			}
		})
	}
}

// seedSchedule creates a schedule of the fixture's template as its owner.
func (f orgFixture) seedSchedule(t *testing.T) string {
	t.Helper()
	var out struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	raw := f.ownerJSON(t, http.MethodPost, "/schedules", map[string]any{
		"template_id": f.template.String(), "name": "nightly", "mode": "cron", "cron": "0 3 * * *", "timezone": "UTC",
	})
	if err := json.Unmarshal(raw, &out); err != nil || out.Schedule.ID == "" {
		t.Fatalf("seed schedule: %v %s", err, raw)
	}
	return out.Schedule.ID
}

func TestScheduleTools(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaTemplatesRead)
	id := f.seedSchedule(t)
	cs := mustConnect(t, f.s, f.token)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_schedules", nil, id},
		{"list_schedules", map[string]any{"template_id": f.template.String()}, id},
		{"get_schedule", map[string]any{"schedule_id": id}, "nightly"},
		{"list_schedule_runs", map[string]any{"schedule_id": id, "limit": 5}, `"runs"`},
	} {
		if text := callOK(t, cs, tc.tool, tc.args, nil); !strings.Contains(text, tc.want) {
			t.Errorf("%s lacks %q:\n%s", tc.tool, tc.want, text)
		}
	}
}

func TestCalendarHidesMaintenanceWithoutMonitoring(t *testing.T) {
	for _, tc := range []struct {
		name  string
		areas []tenancy.Area
		want  bool
	}{
		{"templates.read alone", []tenancy.Area{tenancy.AreaTemplatesRead}, false},
		{"with monitoring.read", []tenancy.Area{tenancy.AreaTemplatesRead, tenancy.AreaMonitoringRead}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgFixture(t, tc.areas...)
			now := time.Now().UTC()
			f.ownerJSON(t, http.MethodPost, "/maintenance-windows", map[string]any{
				"name": "db upgrade", "target_kind": "host", "target_id": f.host.String(),
				"starts_at": now.Add(time.Hour).Format(time.RFC3339), "ends_at": now.Add(2 * time.Hour).Format(time.RFC3339),
			})
			cs := mustConnect(t, f.s, f.token)
			var cal map[string]json.RawMessage
			callOK(t, cs, "get_calendar", map[string]any{"from": now.Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)}, &cal)
			if _, ok := cal["entries"]; !ok {
				t.Errorf("no entries key: %v", cal)
			}
			m, has := cal["maintenance"]
			if has != tc.want {
				t.Fatalf("maintenance present = %v, want %v", has, tc.want)
			}
			if tc.want && !strings.Contains(string(m), "db upgrade") {
				t.Errorf("maintenance lacks the window: %s", m)
			}
		})
	}
}

func TestSetHostTags(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	h := map[string]any{"host_id": f.host.String()}
	callOK(t, cs, "set_host_tags", map[string]any{"host_id": f.host.String(), "tags": []map[string]any{{"key": "env", "value": "prod"}, {"key": "role", "value": "web"}}}, nil)
	text := callOK(t, cs, "get_host", h, nil)
	if !strings.Contains(text, "env") || !strings.Contains(text, "prod") || !strings.Contains(text, "role") {
		t.Fatalf("get_host lacks the tags:\n%s", text)
	}
	// The set replaces the manual tags.
	callOK(t, cs, "set_host_tags", map[string]any{"host_id": f.host.String(), "tags": []map[string]any{{"key": "env", "value": "dev"}}}, nil)
	text = callOK(t, cs, "get_host", h, nil)
	if strings.Contains(text, "role") || !strings.Contains(text, "dev") {
		t.Fatalf("tags were not replaced:\n%s", text)
	}
	// A key-only tag is accepted: the value may be empty.
	callOK(t, cs, "set_host_tags", map[string]any{"host_id": f.host.String(), "tags": []map[string]any{{"key": "db"}}}, nil)
	if text = callOK(t, cs, "get_host", h, nil); !strings.Contains(text, `"db"`) {
		t.Fatalf("key-only tag missing:\n%s", text)
	}
	callOK(t, cs, "set_host_tags", map[string]any{"host_id": f.host.String(), "tags": []map[string]any{}}, nil)
	if text = callOK(t, cs, "get_host", h, nil); strings.Contains(text, `"env"`) {
		t.Fatalf("empty set did not clear:\n%s", text)
	}
}

func TestStartConnectionTest(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	pins := func() string {
		m, err := f.s.Deps.Hosts.PinStates(f.scoped, f.org, []uuid.UUID{f.host})
		if err != nil {
			t.Fatal(err)
		}
		return m[f.host]
	}
	before := pins()
	var out struct {
		JobID string `json:"job_id"`
	}
	callOK(t, cs, "start_connection_test", map[string]any{"host_id": f.host.String()}, &out)
	id, err := uuid.Parse(out.JobID)
	if err != nil {
		t.Fatalf("job_id %q: %v", out.JobID, err)
	}
	job, err := f.s.Deps.Jobs.Get(f.scoped, f.org, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != jobs.KindConnectionTest {
		t.Errorf("job kind = %q, want connection_test", job.Kind)
	}
	if after := pins(); after != before {
		t.Errorf("pin state changed from %q to %q", before, after)
	}
	// No test has completed yet: the read is a refusal, not a crash.
	if text, isErr := call(t, cs, "get_connection_test", map[string]any{"host_id": f.host.String()}); !isErr {
		t.Errorf("get_connection_test before any outcome: %s", text)
	}
	if err := f.s.Deps.Hosts.RecordConnectionTest(f.scoped, f.org, f.host, time.Now(), true, "SHA256:testfingerprint", "", "unpinned",
		[]hosts.HostTestCheck{{Name: "login", Detail: "ok", OK: true}}, nil); err != nil {
		t.Fatal(err)
	}
	text := callOK(t, cs, "get_connection_test", map[string]any{"host_id": f.host.String()}, nil)
	for _, w := range []string{"SHA256:testfingerprint", `"login"`, `"unpinned"`} {
		if !strings.Contains(text, w) {
			t.Errorf("get_connection_test lacks %q:\n%s", w, text)
		}
	}
}

func TestListActivity(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaInventoryWrite, tenancy.AreaActivityRead)
	cs := mustConnect(t, f.s, f.token)
	callOK(t, cs, "set_host_tags", map[string]any{"host_id": f.host.String(), "tags": []map[string]any{{"key": "env", "value": "prod"}}}, nil)
	text := callOK(t, cs, "list_activity", map[string]any{"object_type": "host", "limit": 50}, nil)
	if !strings.Contains(text, "set tags for host") || !strings.Contains(text, `"token_name":"assistant"`) {
		t.Fatalf("activity lacks the token's entry:\n%s", text)
	}
	since := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if text := callOK(t, cs, "list_activity", map[string]any{"since": since}, nil); strings.Contains(text, "set tags for host") {
		t.Errorf("since in the future still lists it:\n%s", text)
	}
}

func TestInstructionsNameTheConnectionTestException(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	res := cs.InitializeResult()
	for _, w := range []string{"start_connection_test", "Playbook runs need a person's approval", "Secrets"} {
		if !strings.Contains(res.Instructions, w) {
			t.Errorf("instructions lack %q:\n%s", w, res.Instructions)
		}
	}
}
