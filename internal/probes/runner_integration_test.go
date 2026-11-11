// The runner against the real database: definitions resolved through
// checks.Service, hosts read through hosts.Service, results landing in
// check_current, and the advisory lock excluding a second runner.
package probes_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probes"
	"github.com/noixdev/pulsix/internal/schedule"
)

// roster is checks.HostRoster over hosts.Service, like internal/app's.
type roster struct{ hosts *hosts.Service }

func (r roster) MonitoredHosts(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	all, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, h := range all {
		if h.Monitored {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

func TestRunnerProbesAndRecords(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "probes", Name: "probes"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: "web",
		Address: "127.0.0.1", Monitored: true, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d), checks.WithHostRoster(roster{hostsSvc}))
	scoped := db.WithOrgScope(ctx, org.ID)
	if _, err := checksSvc.CreateDefinition(scoped, org.ID, checks.DefinitionInput{
		Key: "site", Plugin: checks.PluginHTTP, Params: map[string]any{"url": "http://{address}:" + port + "/"},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &host.ID,
	}); err != nil {
		t.Fatal(err)
	}

	first := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Guard:  probe.NewGuardForTest(true, nil),
		Tick:   20 * time.Millisecond,
	})
	runCtx, stopFirst := context.WithCancel(ctx)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		first.Run(runCtx)
	}()
	defer func() {
		stopFirst()
		<-firstDone
	}()

	// One tick probes the server; check_current then holds http.up = 1.
	var up float64
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := d.Pool().QueryRow(ctx, `SELECT value FROM check_current
			WHERE host_id = $1 AND check_key = 'site' AND item = '' AND metric = 'http.up'`, host.ID).Scan(&up)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no http.up in check_current after 15 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if up != 1 {
		t.Fatalf("http.up = %v, want 1", up)
	}

	// A second runner on its own locker for the same key never probes
	// while the first holds the lock.
	var secondProbes atomic.Int32
	second := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Probe: func(context.Context, string, map[string]any, string) []probe.Observation {
			secondProbes.Add(1)
			return nil
		},
		Tick: 20 * time.Millisecond,
	})
	secondCtx, stopSecond := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stopSecond()
	second.Run(secondCtx)
	if n := secondProbes.Load(); n != 0 {
		t.Fatalf("the second runner probed %d times while the first held the lock", n)
	}

	// Stopping the first releases the lock.
	stopFirst()
	<-firstDone
	after := schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes)
	locked, err := after.TryLock(ctx)
	if err != nil || !locked {
		t.Fatalf("after Run returned: TryLock = %v, %v; the lock must be released", locked, err)
	}
	after.Unlock(ctx)
}
