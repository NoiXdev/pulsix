package probes_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/orgsecrets"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/snmp"
	"github.com/noixdev/pulsix/internal/probes"
	"github.com/noixdev/pulsix/internal/schedule"
)

// secretPoller records the secrets it is handed, per check key.
type secretPoller struct {
	mu      sync.Mutex
	secrets map[string]map[string]string
}

func (p *secretPoller) Poll(_ context.Context, params map[string]any, _ string, secret map[string]string) ([]probe.Observation, snmp.Info) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name, _ := params["secret"].(string)
	p.secrets[name] = secret
	one := 1.0
	return []probe.Observation{{Metric: "snmp.up", Value: &one}}, snmp.Info{}
}

func (p *secretPoller) seen(name string) (map[string]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.secrets[name]
	return s, ok
}

// TestRunnerReadsTheSecretStore: the runner reads a stored secret through
// the real store (sealed, in the organization's scope) and hands it to
// the poll; a check naming a secret the organization does not store
// records the error on its metrics instead.
func TestRunnerReadsTheSecretStore(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "probes-secrets", Name: "probes-secrets"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: "sw",
		Address: "192.0.2.10", Monitored: true, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	secretsSvc := orgsecrets.NewService(orgsecrets.Deps{
		Store: orgsecrets.NewSQLStore(d), Box: crypto.NewBox([32]byte{'p', 'r', 'o', 'b', 'e', 's'}),
		Checks: app.NewSecretCheckRefs(checksSvc),
	})
	scoped := db.WithOrgScope(ctx, org.ID)
	if _, err := secretsSvc.Put(scoped, org.ID, "core-sw", map[string]string{"community": "s3cret-community"}); err != nil {
		t.Fatal(err)
	}
	for key, secret := range map[string]string{"sw": "core-sw", "sw-absent": "absent"} {
		if _, err := checksSvc.CreateDefinition(scoped, org.ID, checks.DefinitionInput{
			Key: key, Plugin: checks.PluginSNMP, Params: map[string]any{"secret": secret, "interfaces": true},
			Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &host.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}

	poller := &secretPoller{secrets: map[string]map[string]string{}}
	runner := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker:    schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		NewPoller: func() probes.Poller { return poller },
		Secrets:   secretsSvc,
		Tick:      20 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(runCtx)
	}()
	defer func() {
		stop()
		<-done
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var msg string
		err := d.Pool().QueryRow(ctx, `SELECT error FROM check_current
			WHERE host_id = $1 AND check_key = 'sw-absent' AND item = '' AND metric = 'snmp.up'`, host.ID).Scan(&msg)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		got, polled := poller.seen("core-sw")
		if err == nil && polled {
			if msg != `secret "absent" is not defined` {
				t.Fatalf("sw-absent's snmp.up error = %q", msg)
			}
			if len(got) != 1 || got["community"] != "s3cret-community" {
				t.Fatalf("the poll got %d fields, want the stored community", len(got))
			}
			if _, ok := poller.seen("absent"); ok {
				t.Fatal("the check naming a missing secret was polled")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 15 s: polled with the stored secret = %v, missing-secret error recorded = %v", polled, err == nil)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
