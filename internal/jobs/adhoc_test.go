// EnqueueAdhoc's tests (backlog 2.14): validation, the per-zone split with
// one shared run group, the sealed payload's shape, and the wire contract
// with the runner's ad-hoc parser (internal/worker/runner/playbook.go's
// adhocPayload reads exactly {"module","argv"}). The fakes come from
// service_test.go, same package.
package jobs_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

func TestEnqueueAdhocSplitsByZoneAndSealsPayload(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zoneA, zoneB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h := func(zone uuid.UUID, managed bool) hosts.Host {
		return hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: managed}
	}
	h1, h2, h3 := h(zoneA, true), h(zoneA, true), h(zoneB, true)
	fh := &fakeHosts{byID: map[uuid.UUID]hosts.Host{h1.ID: h1, h2.ID: h2, h3.ID: h3}}
	store := newFakeStore()
	actor := uuid.Must(uuid.NewV7())
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, fh)

	// A duplicate id must not double-target the host, so h1 is repeated.
	created, err := svc.EnqueueAdhoc(ctx, org, []uuid.UUID{h1.ID, h2.ID, h1.ID, h3.ID}, "shell", "uptime", &actor)
	if err != nil {
		t.Fatalf("EnqueueAdhoc: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("jobs = %d, want one per zone", len(created))
	}
	if created[0].ZoneID != zoneA {
		t.Fatalf("first job zone = %v, want the hosts' first zone", created[0].ZoneID)
	}
	group := created[0].RunGroupID
	if group == uuid.Nil {
		t.Fatal("run group must be a real uuid")
	}
	if created[1].RunGroupID != group {
		t.Fatal("per-zone jobs must share a run group")
	}

	// Every job of the group carries the sealed payload: a JSON string
	// carrying the base64 ciphertext, openable under the job's own AAD.
	for _, j := range created {
		if j.Kind != jobs.KindAdhoc || j.Status != jobs.StatusQueued || j.Trigger != jobs.TriggerManual {
			t.Fatalf("job %s = %+v", j.ID, j)
		}
		if j.ActorID == nil || *j.ActorID != actor {
			t.Fatalf("job %s actor = %v, want %v", j.ID, j.ActorID, actor)
		}
		stored, ok := store.jobs[j.ID]
		if !ok {
			t.Fatalf("job %s was not stored", j.ID)
		}
		var b64 string
		if err := json.Unmarshal(stored.payload, &b64); err != nil {
			t.Fatalf("payload of %s is not a JSON string: %v", j.ID, err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := fakeSealer{}.OpenAAD(raw, jobs.JobAAD(org, j.ID))
		if err != nil {
			t.Fatalf("open payload of %s: %v", j.ID, err)
		}
		if string(plain) != `{"module":"shell","argv":"uptime"}` {
			t.Fatalf("payload of %s = %q", j.ID, plain)
		}
	}
	// The zone A job carries both of its hosts, deduplicated.
	for _, j := range created {
		if j.ZoneID == zoneA {
			if len(j.HostIDs) != 2 {
				t.Fatalf("zone A job hosts = %v", j.HostIDs)
			}
			continue
		}
		if len(j.HostIDs) != 1 || j.HostIDs[0] != h3.ID {
			t.Fatalf("zone B job hosts = %v, want %v", j.HostIDs, []uuid.UUID{h3.ID})
		}
	}
}

// TestEnqueueAdhocPayloadMatchesRunnerContract pins the wire shape the
// worker's ad-hoc parser expects: the runner unmarshals the claim's kind
// payload into {"module","argv"} (internal/worker/runner/playbook.go) and
// refuses anything but module shell. The built payload must read back as
// exactly that pair.
func TestEnqueueAdhocPayloadMatchesRunnerContract(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())
	managed := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: true}
	fh := &fakeHosts{byID: map[uuid.UUID]hosts.Host{managed.ID: managed}}
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, fh)

	argv := "echo 'hi'; [a]: b\n--- inject"
	created, err := svc.EnqueueAdhoc(ctx, org, []uuid.UUID{managed.ID}, "shell", argv, nil)
	if err != nil {
		t.Fatalf("EnqueueAdhoc: %v", err)
	}
	stored := store.jobs[created[0].ID]
	var b64 string
	if err := json.Unmarshal(stored.payload, &b64); err != nil {
		t.Fatalf("payload is not a JSON string: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fakeSealer{}.OpenAAD(raw, jobs.JobAAD(org, created[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	// The runner's own parse shape, replicated here as the contract:
	// module and argv as plain JSON strings, the argv round-tripping
	// verbatim so YAML-special characters reach the playbook intact.
	var wire struct {
		Module string `json:"module"`
		Argv   string `json:"argv"`
	}
	if err := json.Unmarshal(plain, &wire); err != nil {
		t.Fatalf("payload is not the runner's shape: %v (%s)", err, plain)
	}
	if wire.Module != "shell" {
		t.Fatalf("module = %q, want shell", wire.Module)
	}
	if wire.Argv != argv {
		t.Fatalf("argv = %q, want the operator's command verbatim", wire.Argv)
	}
}

func TestEnqueueAdhocValidatesInput(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zone := uuid.Must(uuid.NewV7())
	managed := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: true}
	unmanaged := hosts.Host{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Managed: false}
	fh := &fakeHosts{byID: map[uuid.UUID]hosts.Host{managed.ID: managed, unmanaged.ID: unmanaged}}

	cases := []struct {
		name    string
		module  string
		argv    string
		hostIDs []uuid.UUID
		want    error
	}{
		{"module raw", "raw", "uptime", []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"module empty", "", "uptime", []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"argv empty", "shell", "", []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"argv whitespace", "shell", "   \t\n", []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"argv oversized", "shell", strings.Repeat("x", 8<<10+1), []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"argv invalid utf-8", "shell", "\xff\xfe", []uuid.UUID{managed.ID}, jobs.ErrInvalidInput},
		{"no hosts", "shell", "uptime", nil, jobs.ErrInvalidInput},
		{"unknown host", "shell", "uptime", []uuid.UUID{uuid.Must(uuid.NewV7())}, jobs.ErrNotFound},
		// An unmanaged host is as invisible as an unknown one: it cannot
		// run anything, so the API answers 404, not a validation error.
		{"unmanaged host", "shell", "uptime", []uuid.UUID{unmanaged.ID}, jobs.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, fh)
			created, err := svc.EnqueueAdhoc(ctx, org, c.hostIDs, c.module, c.argv, nil)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(created) != 0 {
				t.Fatalf("a failed enqueue must create no jobs, got %d", len(created))
			}
			if len(store.jobs) != 0 {
				t.Fatalf("a failed enqueue must store nothing, got %d job(s)", len(store.jobs))
			}
		})
	}
}
