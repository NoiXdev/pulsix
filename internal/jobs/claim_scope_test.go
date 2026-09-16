// White-box tests for the claim-time scope of host-bound jobs (final
// review C1): a run or ad-hoc claim renders exactly the job's own host ids
// — never the template's scope, which the enqueue already resolved and
// split per zone — and re-applies the changed-host-key exclusion, because
// a key can change between enqueue and claim.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// scopeRecordingRenderer records every scope it is asked to render and
// renders only the connections that scope names — the property the claim
// depends on, which the value fakes elsewhere ignore.
type scopeRecordingRenderer struct {
	mu     sync.Mutex
	all    map[string]render.Connection
	scopes []render.Scope
}

func (r *scopeRecordingRenderer) Snapshot(_ context.Context, _ uuid.UUID, scope render.Scope) (render.Inventory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scope)
	conns := map[string]render.Connection{}
	hostvars := map[string]any{}
	for name, c := range r.all {
		if scope.GroupID == nil && len(scope.HostIDs) > 0 && !slices.Contains(scope.HostIDs, c.HostID) {
			continue
		}
		conns[name] = c
		hostvars[name] = map[string]any{"ansible_host": name}
	}
	return render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": hostvars}},
		Connections: conns,
	}, nil
}

func (r *scopeRecordingRenderer) recorded() []render.Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]render.Scope(nil), r.scopes...)
}

// fakeChangedPins is a pin source that also answers ChangedHosts, the
// shape hosts.Service has.
type fakeChangedPins struct {
	fakePins
	changed map[uuid.UUID]bool
	err     error
}

func (f fakeChangedPins) ChangedHosts(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f.changed[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestClaimRendersExactlyTheJobsHosts(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	h1, h2, h3 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	groupID := uuid.Must(uuid.NewV7())
	all := map[string]render.Connection{
		"web1": {HostID: h1}, "web2": {HostID: h2}, "db1": {HostID: h3},
	}

	cases := []struct {
		name      string
		kind      Kind
		scope     string // the template's stored scope
		jobHosts  []uuid.UUID
		changed   map[uuid.UUID]bool
		wantHosts []uuid.UUID
		wantConns []string
		wantErr   error
	}{
		{
			name: "whole-org template, job for one host renders one host", kind: KindRun,
			scope: `{}`, jobHosts: []uuid.UUID{h1},
			wantHosts: []uuid.UUID{h1}, wantConns: []string{"web1"},
		},
		{
			name: "group template, job for two hosts renders those two", kind: KindRun,
			scope: `{"group_id":"` + groupID.String() + `"}`, jobHosts: []uuid.UUID{h1, h2},
			wantHosts: []uuid.UUID{h1, h2}, wantConns: []string{"web1", "web2"},
		},
		{
			name: "retry narrowed to the failed host renders only it", kind: KindRun,
			scope: `{"host_ids":["` + h1.String() + `","` + h2.String() + `","` + h3.String() + `"]}`, jobHosts: []uuid.UUID{h3},
			wantHosts: []uuid.UUID{h3}, wantConns: []string{"db1"},
		},
		{
			name: "a host whose key changed after enqueue is excluded at claim", kind: KindRun,
			scope: `{}`, jobHosts: []uuid.UUID{h1, h2}, changed: map[uuid.UUID]bool{h2: true},
			wantHosts: []uuid.UUID{h1}, wantConns: []string{"web1"},
		},
		{
			name: "every host changed fails the claim", kind: KindRun,
			scope: `{}`, jobHosts: []uuid.UUID{h1}, changed: map[uuid.UUID]bool{h1: true},
			wantErr: ErrHostKeyChanged,
		},
		{
			name: "ad-hoc renders its hosts and honors the guard", kind: KindAdhoc,
			jobHosts: []uuid.UUID{h1, h3}, changed: map[uuid.UUID]bool{h3: true},
			wantHosts: []uuid.UUID{h1}, wantConns: []string{"web1"},
		},
		{
			name: "a run job without hosts fails closed instead of rendering the org", kind: KindRun,
			scope: `{}`, jobHosts: nil, wantErr: ErrInvalidInput,
		},
		{
			name: "an ad-hoc job without hosts fails closed", kind: KindAdhoc,
			jobHosts: nil, wantErr: ErrInvalidInput,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			renderer := &scopeRecordingRenderer{all: all}
			templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
				templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all", Scope: json.RawMessage(c.scope)},
			}}
			q := newTestQueue(renderer, fakeCreds{}, templates, fakeBastions{})
			q.SetKeyPins(fakeChangedPins{changed: c.changed})
			job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: c.kind, HostIDs: c.jobHosts}
			if c.kind == KindRun {
				job.TemplateID = &templateID
			} else {
				job.Payload = json.RawMessage(`{"module":"shell","argv":"true"}`)
			}
			out := &workerpb.Job{}
			err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("buildPayload error = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			scopes := renderer.recorded()
			if len(scopes) != 1 {
				t.Fatalf("rendered %d times, want once: %+v", len(scopes), scopes)
			}
			if scopes[0].GroupID != nil || !slices.Equal(scopes[0].HostIDs, c.wantHosts) {
				t.Fatalf("rendered scope = %+v, want exactly hosts %v", scopes[0], c.wantHosts)
			}
			var got []string
			for name := range out.Connections {
				got = append(got, name)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.wantConns) {
				t.Fatalf("claim connections = %v, want %v", got, c.wantConns)
			}
		})
	}
}

func TestClaimKeyGuardFailureFailsClosed(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	h1 := uuid.Must(uuid.NewV7())
	q := newTestQueue(&scopeRecordingRenderer{all: map[string]render.Connection{"web1": {HostID: h1}}}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakeChangedPins{err: errors.New("db down")})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindAdhoc, HostIDs: []uuid.UUID{h1},
		Payload: json.RawMessage(`{"module":"shell","argv":"true"}`)}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil {
		t.Fatal("a failing key guard must fail the claim closed")
	}
}
