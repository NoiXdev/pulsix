package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

const hostFilterIndex = "job_hosts_host_idx"

// generatedQuery returns the raw SQL of the const constName declared in
// internal/db/gen/jobs.sql.go, parsed from the source so the plan tests
// explain exactly the statement the store sends.
func generatedQuery(t *testing.T, constName string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../db/gen/jobs.sql.go", nil, 0)
	if err != nil {
		t.Fatalf("parse generated jobs queries: %v", err)
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.Name != constName || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("const %s is not a string literal", constName)
				}
				sql, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote const %s: %v", constName, err)
				}
				return sql
			}
		}
	}
	t.Fatalf("const %s not found in internal/db/gen/jobs.sql.go", constName)
	return ""
}

// planUsesIndex walks an EXPLAIN (FORMAT JSON) document for a node that
// reads the named index.
func planUsesIndex(node any, index string) bool {
	switch n := node.(type) {
	case map[string]any:
		if n["Index Name"] == index {
			return true
		}
		for _, v := range n {
			if planUsesIndex(v, index) {
				return true
			}
		}
	case []any:
		for _, v := range n {
			if planUsesIndex(v, index) {
				return true
			}
		}
	}
	return false
}

// jobHostsBackfill is the statement migration 00082 fills job_hosts with
// from the run history: one link per distinct (job, host) pair whose host
// still exists in the job's organization, carrying the job's created_at.
const jobHostsBackfill = `
INSERT INTO job_hosts (org_id, job_id, host_id, created_at)
SELECT DISTINCT j.org_id, j.id, h.id, j.created_at
FROM jobs j
CROSS JOIN LATERAL unnest(j.host_ids) AS t(host_id)
JOIN hosts h ON h.org_id = j.org_id AND h.id = t.host_id`

// seedHostFilterJobs fills one organization with 200 hosts and 50 000
// jobs, each targeting one to three of them and spread over 50 000
// minutes, links them through jobHostsBackfill and analyzes both tables
// so the planner sees realistic statistics. It returns
// the organization, one host id and a pool bound to pulsix_app (RLS in
// force, as in production); the seed and ANALYZE run as the superuser.
func seedHostFilterJobs(t *testing.T) (org, host uuid.UUID, app *db.DB) {
	t.Helper()
	ctx := context.Background()
	app = testdb.Open(t)
	owner, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)

	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := gen.New(owner.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: org, Slug: "hostplan", Name: "hostplan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, 'z', 'z')`, zone, org); err != nil {
		t.Fatal(err)
	}
	hosts := make([]uuid.UUID, 200)
	for i := range hosts {
		hosts[i] = uuid.Must(uuid.NewV7())
	}
	if _, err := owner.Pool().Exec(ctx, `
INSERT INTO hosts (id, org_id, zone_id, name)
SELECT h.id, $1, $2, 'h-' || h.n
FROM unnest($3::uuid[]) WITH ORDINALITY AS h(id, n)`, org, zone, hosts); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Pool().Exec(ctx, `
INSERT INTO jobs (id, org_id, zone_id, kind, host_ids, run_group_id, created_at)
SELECT gen_random_uuid(), $1, $2, 'run',
       ARRAY(SELECT ($3::uuid[])[1 + (random() * 199)::int] FROM generate_series(1, 1 + (g % 3))),
       gen_random_uuid(), now() - make_interval(mins => g)
FROM generate_series(1, 50000) AS g`, org, zone, hosts); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Pool().Exec(ctx, jobHostsBackfill); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Pool().Exec(ctx, `ANALYZE jobs, job_hosts`); err != nil {
		t.Fatal(err)
	}
	return org, hosts[0], app
}

// explainGeneric runs explainGenericOn on a connection acquired from the
// app pool and fails the test on an error.
func explainGeneric(t *testing.T, app *db.DB, org uuid.UUID, query, execArgs string, analyze bool) (plan any, millis float64) {
	t.Helper()
	ctx := context.Background()
	conn, err := app.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	plan, millis, err = explainGenericOn(ctx, conn.Conn(), org, query, execArgs, analyze)
	if err != nil {
		t.Fatal(err)
	}
	return plan, millis
}

// explainGenericOn prepares query in a transaction on conn bound to org,
// forces a generic plan (what a prepared statement settles on after five
// executions, and what pgx's extended protocol reaches in production) and
// returns the EXPLAIN JSON of executing it with execArgs. With analyze it
// also runs the statement and returns the execution time in milliseconds.
func explainGenericOn(ctx context.Context, conn *pgx.Conn, org uuid.UUID, query, execArgs string, analyze bool) (plan any, millis float64, err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	// A prepared statement belongs to the session, not the transaction:
	// deallocate it after the rollback, outside the transaction (inside a
	// failed one the DEALLOCATE itself is refused), or the next call on the
	// same connection fails to prepare it again. The error is ignored: the
	// PREPARE may never have happened.
	defer func() {
		_ = tx.Rollback(ctx)
		_, _ = conn.Exec(ctx, `DEALLOCATE list_jobs`)
	}()
	for _, stmt := range []string{
		`SET LOCAL plan_cache_mode = force_generic_plan`,
		`SELECT set_config('pulsix.org_id', '` + org.String() + `', true)`,
		`PREPARE list_jobs AS ` + query,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return nil, 0, fmt.Errorf("%.40s: %w", stmt, err)
		}
	}
	opts := "FORMAT JSON"
	if analyze {
		opts = "ANALYZE, FORMAT JSON"
	}
	var raw []byte
	if err := tx.QueryRow(ctx, fmt.Sprintf(`EXPLAIN (%s) EXECUTE list_jobs(%s)`, opts, execArgs)).Scan(&raw); err != nil {
		return nil, 0, fmt.Errorf("explain: %w", err)
	}
	var doc []map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc) != 1 {
		return nil, 0, fmt.Errorf("explain output %s: %v", raw, err)
	}
	if v, ok := doc[0]["Execution Time"].(float64); ok {
		millis = v
	}
	return doc[0]["Plan"], millis, nil
}

// TestListJobsHostFilterUsesTheLinkIndexUnderAGenericPlan pins backlog
// 7.84: the runs list of one host reads the newest links of that host from
// job_hosts_host_idx (migration 00082) instead of the organization's whole
// run history. The GIN index jobs_host_ids_idx of 00079 (backlog 7.73)
// never served the app role: row-level security only lets a user condition
// into an index scan ahead of the policy's own when its operator is
// leakproof, and array containment (@>) is not, so as pulsix_app the
// planner scanned every job of the organization and filtered afterwards.
// uuid equality is leakproof, so the link table's host_id = $2 is an index
// condition under RLS, and the index's (created_at, job_id) order serves
// the list's ORDER BY and keyset cursor without a sort. Every optional
// filter is written "$n IS NULL OR ...", which a generic plan cannot fold
// away, so the plan is measured on the exact query the store runs, as
// pulsix_app under force_generic_plan, not assumed.
func TestListJobsHostFilterUsesTheLinkIndexUnderAGenericPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50 000 jobs")
	}
	query := generatedQuery(t, "listJobsForHost")
	org, host, app := seedHostFilterJobs(t)

	// gen.ListJobsForHostParams order: org, host, zone, status, kind,
	// template, run group, trigger, trigger ref, cursor at, cursor id, limit.
	args := fmt.Sprintf(`'%s', '%s', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 51`, org, host)

	plan, _ := explainGeneric(t, app, org, query, args, false)
	pretty, _ := json.MarshalIndent(plan, "", " ")
	t.Logf("generic plan:\n%s", pretty)
	if !planUsesIndex(plan, hostFilterIndex) {
		t.Fatalf("generic plan does not use %s", hostFilterIndex)
	}
	_, ms := explainGeneric(t, app, org, query, args, true)
	t.Logf("execution time under the generic plan: %.2f ms", ms)
}

// TestExplainGenericFreesItsStatementAfterAFailure: a failed call must not
// leave the prepared statement behind on the connection, or the next call
// on the same pooled connection dies on "already exists" instead of running.
// The first call must fail at the EXPLAIN EXECUTE, after the PREPARE went
// through: any earlier failure leaves nothing to clean up and proves nothing.
// 22P02 (invalid_text_representation) is the bad uuid argument.
func TestExplainGenericFreesItsStatementAfterAFailure(t *testing.T) {
	ctx := context.Background()
	app := testdb.Open(t)
	owner, err := db.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	org := uuid.Must(uuid.NewV7())
	if _, err := gen.New(owner.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: org, Slug: "plancleanup", Name: "plancleanup"}); err != nil {
		t.Fatal(err)
	}
	conn, err := app.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	query := generatedQuery(t, "listJobsForHost")
	const tail = `, '` + "00000000-0000-0000-0000-000000000000" + `', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 51`
	_, _, err = explainGenericOn(ctx, conn.Conn(), org, query, `'not-a-uuid'`+tail, false)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22P02" || !strings.HasPrefix(err.Error(), "explain: ") {
		t.Fatalf("first call: error = %v, want an \"explain: \" error with code 22P02 (invalid input for the uuid argument)", err)
	}
	if _, _, err := explainGenericOn(ctx, conn.Conn(), org, query, `'`+org.String()+`'`+tail, false); err != nil {
		t.Fatalf("second call on the same connection: %v", err)
	}
}
