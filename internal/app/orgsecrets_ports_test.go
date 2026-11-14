package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/orgsecrets"
)

// TestSecretCheckRefsEveryBinding: a secret named by a check at each
// binding — the organization's, a monitoring group's, a host's — and by a
// shared definition's projection is held by all four, and Delete names
// them. The plugin is mysql under a lookup that runs it on the server (no
// real server plugin takes a secret yet); under the real descriptors mysql
// runs on the agent, which reads agent.yaml, so nothing holds the secret.
func TestSecretCheckRefsEveryBinding(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-secret-refs")
	scoped := db.WithOrgScope(ctx, org)
	q := gen.New(d.Pool())
	params := []byte(`{"secret":"sw"}`)

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	zone, err := hostsSvc.CreateZone(scoped, org, "edge", "Edge")
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "db-1", Address: "192.0.2.10", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	group, err := q.CreateMonitoringGroup(ctx, gen.CreateMonitoringGroupParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "dbs", Selector: "*", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range []gen.CreateCheckDefinitionParams{
		{Key: "at-org", BindingKind: "org"},
		{Key: "at-group", BindingKind: "group", GroupID: pgtype.UUID{Bytes: group.ID, Valid: true}},
		{Key: "at-host", BindingKind: "host", HostID: pgtype.UUID{Bytes: host.ID, Valid: true}},
	} {
		def.ID, def.OrgID, def.Plugin, def.Params, def.IntervalSeconds, def.Enabled, def.Origin = uuid.Must(uuid.NewV7()), org, "mysql", params, 60, true, "custom"
		if _, err := q.CreateCheckDefinition(ctx, def); err != nil {
			t.Fatal(err)
		}
	}
	shared, err := q.CreateSharedCheckDefinition(ctx, gen.CreateSharedCheckDefinitionParams{ID: uuid.Must(uuid.NewV7()), Key: "shared-def", Plugin: "mysql", Params: params, IntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertProjectedCheckDefinition(ctx, gen.InsertProjectedCheckDefinitionParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Key: shared.Key, Plugin: "mysql", Params: params, IntervalSeconds: 60,
		SharedDefinitionID: pgtype.UUID{Bytes: shared.ID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	refs := NewSecretCheckRefs(checks.NewService(checks.NewSQLStore(d)))
	mysqlOnServer := func(name string) (checkplugins.Descriptor, bool) {
		desc, ok := checkplugins.Get(name)
		if name == "mysql" {
			desc.Runs = checkplugins.Server
		}
		return desc, ok
	}
	build := func(lookup func(string) (checkplugins.Descriptor, bool)) *orgsecrets.Service {
		return orgsecrets.NewService(orgsecrets.Deps{
			Store: orgsecrets.NewSQLStore(d), Box: crypto.NewBox([32]byte{1}), Checks: refs, Plugins: lookup,
		})
	}
	onServer := build(mysqlOnServer)
	if err := onServer.Put(scoped, org, "sw", map[string]string{"password": "x"}); err != nil {
		t.Fatal(err)
	}

	err = onServer.Delete(scoped, org, "sw")
	if !errors.Is(err, orgsecrets.ErrConflict) || !strings.Contains(err.Error(), "at-group, at-host, at-org, shared-def") {
		t.Fatalf("Delete = %v, want ErrConflict naming at-group, at-host, at-org, shared-def", err)
	}
	list, err := onServer.List(scoped, org)
	if err != nil || len(list) != 1 || strings.Join(list[0].UsedBy, ",") != "at-group,at-host,at-org,shared-def" {
		t.Fatalf("List = %+v, %v", list, err)
	}

	agentRun := build(nil)
	if list, err := agentRun.List(scoped, org); err != nil || len(list) != 1 || len(list[0].UsedBy) != 0 {
		t.Fatalf("List with the real descriptors = %+v, %v; want sw used by nothing (mysql runs on the agent)", list, err)
	}
	if err := agentRun.Delete(scoped, org, "sw"); err != nil {
		t.Fatalf("Delete with the real descriptors: %v", err)
	}
}
