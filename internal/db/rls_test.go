package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestMembershipsAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	for _, o := range []struct {
		id   uuid.UUID
		slug string
	}{{orgA, "a"}, {orgB, "b"}} {
		if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.id, Slug: o.slug, Name: o.slug}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: user, Email: "a@example.test", DisplayName: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []uuid.UUID{orgA, orgB} {
		if err := q.AddMembership(ctx, gen.AddMembershipParams{OrgID: o, UserID: user, Role: gen.OrgRoleOwner}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM org_memberships`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
	}
}

// TestEscalationTablesAreIsolatedPerOrg exercises query-time visibility for
// the three tables migration 00056 added. The schema test of the same name's
// prefix (TestEscalationTablesEnforceTenancy, schema_test.go) only shows the
// composite foreign key rejects a row naming a mismatched org — it inserts
// and reads with no session org set at all, so it never actually runs a
// query through the org_isolation policy. This test does, the same way
// TestMembershipsAreIsolatedPerOrg above does for org_memberships.
func TestEscalationTablesAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	hostA, hostB := seedHost(t, d, orgA), seedHost(t, d, orgB)
	routeA, routeB := seedAlertRoute(t, d, orgA), seedAlertRoute(t, d, orgB)
	chA, chB := seedChannel(t, d, orgA), seedChannel(t, d, orgB)
	alertA, alertB := seedOpenAlert(t, d, orgA, hostA), seedOpenAlert(t, d, orgB, hostB)

	for _, s := range []struct {
		org   uuid.UUID
		route uuid.UUID
		ch    uuid.UUID
		alert uuid.UUID
	}{
		{orgA, routeA, chA, alertA},
		{orgB, routeB, chB, alertB},
	} {
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO alert_route_steps (org_id, route_id, position, delay_seconds)
			VALUES ($1, $2, 1, 300)`, s.org, s.route); err != nil {
			t.Fatalf("insert step: %v", err)
		}
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO alert_route_step_channels (org_id, route_id, position, channel_id)
			VALUES ($1, $2, 1, $3)`, s.org, s.route, s.ch); err != nil {
			t.Fatalf("insert step channel: %v", err)
		}
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO alert_escalations (org_id, alert_id, route_id, position, fired_at)
			VALUES ($1, $2, $3, 0, now())`, s.org, s.alert, s.route); err != nil {
			t.Fatalf("insert escalation: %v", err)
		}
	}

	t.Run("alert_route_steps", func(t *testing.T) {
		var seen []uuid.UUID
		err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT org_id FROM alert_route_steps`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
		}
	})

	t.Run("alert_route_step_channels", func(t *testing.T) {
		var seen []uuid.UUID
		err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT org_id FROM alert_route_step_channels`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
		}
	})

	t.Run("alert_escalations", func(t *testing.T) {
		var seen []uuid.UUID
		err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT org_id FROM alert_escalations`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
		}
	})
}

// TestVaultCredentialsAreIsolatedPerOrg exercises query-time visibility for
// the two tables migration 00059 added, the same gap
// TestEscalationTablesAreIsolatedPerOrg's own comment names for 00056:
// schema_test.go's TestGitRepoVaultCredentialsSchema and
// TestJobTemplateVaultCredentialsSchema insert and read through
// d.Pool() with no session org bound at all, so a composite foreign key
// rejecting a cross-tenant insert is the only thing either one actually
// proves — neither ever runs a SELECT through the org_isolation policy,
// and both would still pass with RLS switched off entirely. This test
// seeds both organizations' rows through the bypass path (same d.Pool(),
// no org bound) and then reads through d.WithOrg, the same shape
// TestMembershipsAreIsolatedPerOrg and TestEscalationTablesAreIsolatedPerOrg
// above use.
func TestVaultCredentialsAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	repoA, repoB := seedGitRepo(t, d, orgA, "repo-a"), seedGitRepo(t, d, orgB, "repo-b")
	tmplA, tmplB := seedJobTemplate(t, d, orgA, "tmpl-a"), seedJobTemplate(t, d, orgB, "tmpl-b")
	credA, credB := seedVaultCredential(t, d, orgA, "cred-a"), seedVaultCredential(t, d, orgB, "cred-b")

	for _, s := range []struct {
		org  uuid.UUID
		repo uuid.UUID
		tmpl uuid.UUID
		cred uuid.UUID
	}{
		{orgA, repoA, tmplA, credA},
		{orgB, repoB, tmplB, credB},
	} {
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
			VALUES ($1, $2, $3, 'default')`, s.org, s.repo, s.cred); err != nil {
			t.Fatalf("insert repo link: %v", err)
		}
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label)
			VALUES ($1, $2, $3, 'default')`, s.org, s.tmpl, s.cred); err != nil {
			t.Fatalf("insert template link: %v", err)
		}
	}

	t.Run("git_repo_vault_credentials", func(t *testing.T) {
		var seen []uuid.UUID
		err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT org_id FROM git_repo_vault_credentials`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
		}
	})

	t.Run("job_template_vault_credentials", func(t *testing.T) {
		var seen []uuid.UUID
		err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT org_id FROM job_template_vault_credentials`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
		}
	})
}

// TestSharedRepoProjectionsAreIsolatedPerOrg is migration 00060's version of
// the gap TestVaultCredentialsAreIsolatedPerOrg's own comment names for
// 00059: schema_test.go's TestSharedReposSchema only shows shared_repos
// carries no org_id and no row-level security of its own — it never proves
// that git_repos, which DOES carry both, still isolates a row that happens
// to project a shared definition. The whole point of "the installation owns
// a definition, each organization gets a projection" (spec decision 1) is
// that the projection is ordinary tenant data: two organizations project
// the SAME shared_repos row here, and a session scoped to org A must still
// see only its own git_repos row, exactly as it would for any other
// git_repos row that named no shared_repo_id at all.
func TestSharedRepoProjectionsAreIsolatedPerOrg(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)

	var shared uuid.UUID
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO shared_repos (id, name, url, branch)
		VALUES (gen_random_uuid(), 'infra-playbooks', 'https://example.test/infra.git', 'main')
		RETURNING id`).Scan(&shared); err != nil {
		t.Fatalf("insert shared_repos: %v", err)
	}

	for _, s := range []struct {
		org  uuid.UUID
		name string
	}{{orgA, "repo-a"}, {orgB, "repo-b"}} {
		if _, err := d.Pool().Exec(ctx, `
			INSERT INTO git_repos (id, org_id, name, url, shared_repo_id)
			VALUES (gen_random_uuid(), $1, $2, 'https://example.test/' || $2 || '.git', $3)`,
			s.org, s.name, shared); err != nil {
			t.Fatalf("insert git_repos projection: %v", err)
		}
	}

	var seen []uuid.UUID
	err := d.WithOrg(ctx, orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT org_id FROM git_repos WHERE shared_repo_id = $1`, shared)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != orgA {
		t.Fatalf("RLS leak: saw %v, want only %s", seen, orgA)
	}
}
