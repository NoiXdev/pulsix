package runrequests_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

type zoneList []hosts.Zone

func (z zoneList) ListZones(context.Context, uuid.UUID) ([]hosts.Zone, error) { return z, nil }

// TestRunNamesTheZonesAReapprovalWouldLeaveOut is backlog 2.142 at the
// resolver: the run request's run opts into the replay's check, and a
// refusal comes back with the zones' and hosts' names (an unknown one by
// id), so the approval page can say which hosts need a new request.
func TestRunNamesTheZonesAReapprovalWouldLeaveOut(t *testing.T) {
	x, zone, gone, web3, lost := id(), id(), id(), id(), id()
	fx := &resolverFx{org: id()}
	fx.jobs = &fakeJobRunner{calls: &fx.calls, group: id(), runErr: &jobs.CoveredZoneGrowthError{Zones: []jobs.ZoneTargets{
		{ZoneID: zone, HostIDs: []uuid.UUID{web3, lost}}, {ZoneID: gone, HostIDs: []uuid.UUID{web3}},
	}}}
	fx.res = runrequests.NewResolver(runrequests.Sources{
		Templates: fakeTemplates{x: templates.Template{ID: x, Name: "X"}}, Jobs: fx.jobs, Credentials: fakeCreds{},
		Settings: fakeSettings{}, FollowUpRuns: fakeFollowUpRuns{calls: &fx.calls},
		Hosts: hostList{hs: []hosts.Host{{ID: web3, Name: "web-3"}}}, Zones: zoneList{{ID: zone, Name: "eu"}},
	})

	err := fx.res.Run(context.Background(), fx.org, runrequests.Request{Kind: runrequests.KindRun, TemplateID: x}, id(), "run-request:x", id(), nil)
	var grew *runrequests.CoveredZonesError
	if !errors.As(err, &grew) || !errors.Is(err, runrequests.ErrCoveredZonesGrew) {
		t.Fatalf("run = %v, want a CoveredZonesError", err)
	}
	if !fx.jobs.enqueued.RefuseCoveredZoneGrowth {
		t.Fatal("the run request's run did not opt into the covered zone check")
	}
	want := []runrequests.CoveredZone{
		{ID: zone, Name: "eu", Hosts: []runrequests.NamedHost{{ID: web3, Name: "web-3"}, {ID: lost, Name: lost.String()}}},
		{ID: gone, Name: gone.String(), Hosts: []runrequests.NamedHost{{ID: web3, Name: "web-3"}}},
	}
	if len(grew.Zones) != 2 || grew.Zones[0].Name != "eu" || len(grew.Zones[0].Hosts) != 2 ||
		grew.Zones[0].Hosts[0] != want[0].Hosts[0] || grew.Zones[0].Hosts[1] != want[0].Hosts[1] ||
		grew.Zones[1].Name != want[1].Name || grew.Zones[1].Hosts[0] != want[1].Hosts[0] {
		t.Fatalf("zones = %+v, want %+v", grew.Zones, want)
	}
	if msg := err.Error(); !strings.Contains(msg, `zone "eu": web-3, `+lost.String()) || !strings.Contains(msg, "request a new run") {
		t.Fatalf("message = %q", msg)
	}
}
