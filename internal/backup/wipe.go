package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// wipeSteps drop public's own objects kind by kind, in an order where each
// kind's dependants are already gone: views before the tables they read,
// tables (with their indexes, constraints, policies, triggers and owned
// sequences) before the functions and types their columns and indexes use.
// Each query yields ready-to-run DROP statements; IF EXISTS and CASCADE
// cover objects an earlier drop already took with it. Extension members are
// skipped everywhere: the extensions (TimescaleDB, citext) stay installed,
// and so does the public schema itself.
var wipeSteps = []struct {
	what  string
	query string
}{
	{"views", `
		SELECT format('DROP %s IF EXISTS public.%I CASCADE',
			CASE c.relkind WHEN 'm' THEN 'MATERIALIZED VIEW' ELSE 'VIEW' END, c.relname)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('v', 'm')
			AND ` + notExtension("pg_class", "c.oid")},
	{"tables", `
		SELECT format('DROP TABLE IF EXISTS public.%I CASCADE', c.relname)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p')
			AND ` + notExtension("pg_class", "c.oid")},
	{"sequences", `
		SELECT format('DROP SEQUENCE IF EXISTS public.%I CASCADE', c.relname)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'S'
			AND ` + notExtension("pg_class", "c.oid")},
	{"functions", `
		SELECT format('DROP %s IF EXISTS %s CASCADE',
			CASE p.prokind WHEN 'a' THEN 'AGGREGATE' WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,
			p.oid::regprocedure)
		FROM pg_proc p
		WHERE p.pronamespace = 'public'::regnamespace
			AND ` + notExtension("pg_proc", "p.oid")},
	{"types", `
		SELECT format('DROP %s IF EXISTS public.%I CASCADE',
			CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, t.typname)
		FROM pg_type t
		WHERE t.typnamespace = 'public'::regnamespace
			AND t.typtype IN ('e', 'c', 'd', 'r')
			AND (t.typrelid = 0 OR (SELECT c.relkind FROM pg_class c WHERE c.oid = t.typrelid) = 'c')
			AND ` + notExtension("pg_type", "t.oid")},
}

func notExtension(class, oid string) string {
	return fmt.Sprintf(notExtensionMember, class, oid)
}

// wipe drops every object of public that is not an extension member,
// goose's table included, in one transaction: either the schema is empty
// afterwards or it is as it was. It never drops the schema and never
// touches an extension, so the schema owner can run it without superuser
// rights.
func wipe(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("wipe: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	for _, step := range wipeSteps {
		rows, err := tx.Query(ctx, step.query)
		if err != nil {
			return fmt.Errorf("wipe: list %s: %w", step.what, err)
		}
		drops, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("wipe: list %s: %w", step.what, err)
		}
		for _, drop := range drops {
			if _, err := tx.Exec(ctx, drop); err != nil {
				return fmt.Errorf("wipe: %s: %w", drop, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("wipe: commit: %w", err)
	}
	return nil
}
