package backup

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// querier is what the catalogue reads need: a transaction or a pool.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// gooseTable is goose's bookkeeping table. It is never dumped or loaded: the
// target's own migrations write it.
const gooseTable = "goose_db_version"

// notExtensionMember is a format for a condition that excludes objects an
// extension owns (TimescaleDB's and citext's functions and types live in
// public too). Its verbs are the catalogue the object lives in and the SQL
// expression of its oid.
const notExtensionMember = `NOT EXISTS (SELECT 1 FROM pg_depend d
	WHERE d.classid = '%s'::regclass AND d.objid = %s AND d.deptype = 'e')`

// tableInfo is one table to dump or load and the columns COPY moves.
type tableInfo struct {
	name    string
	columns []string
}

// listTables returns every ordinary or partitioned table of public, except
// goose's, partitions (their rows are read through the parent) and extension
// members, with its stored columns in attnum order. Generated columns are
// left out: the target computes them. Hypertables are ordinary tables here;
// their chunks live in TimescaleDB's own schemas and are never listed.
func listTables(ctx context.Context, q querier) ([]tableInfo, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname, array_agg(a.attname::text ORDER BY a.attnum)
		FROM pg_class c
		JOIN pg_attribute a ON a.attrelid = c.oid
			AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
		WHERE c.relnamespace = 'public'::regnamespace
			AND c.relkind IN ('r', 'p')
			AND NOT c.relispartition
			AND c.relname <> $1
			AND `+notExtension("pg_class", "c.oid")+`
		GROUP BY c.relname
		ORDER BY c.relname`, gooseTable)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (tableInfo, error) {
		var t tableInfo
		err := row.Scan(&t.name, &t.columns)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return out, nil
}

// listSequences returns the names of public's sequences, except goose's own
// and extension members.
func listSequences(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT c.relname FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'S'
			AND `+notExtension("pg_class", "c.oid")+`
			AND NOT EXISTS (SELECT 1 FROM pg_depend d JOIN pg_class owner ON owner.oid = d.refobjid
				WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('a', 'i')
					AND owner.relnamespace = 'public'::regnamespace AND owner.relname = $1)
		ORDER BY c.relname`, gooseTable)
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	return names, nil
}

// readSequences returns the state of every sequence listSequences finds.
func readSequences(ctx context.Context, q querier) ([]SequenceValue, error) {
	names, err := listSequences(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]SequenceValue, 0, len(names))
	for _, name := range names {
		s := SequenceValue{Name: name}
		if err := q.QueryRow(ctx, "SELECT last_value, is_called FROM "+qualified(name)).Scan(&s.LastValue, &s.IsCalled); err != nil {
			return nil, fmt.Errorf("read sequence %s: %w", name, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// foreignKey is one foreign-key constraint as the catalogue defines it.
type foreignKey struct {
	table, name, definition string
}

// listForeignKeys returns every foreign key declared on a public table, with
// the definition pg_get_constraintdef gives for re-adding it unchanged.
func listForeignKeys(ctx context.Context, q querier) ([]foreignKey, error) {
	rows, err := q.Query(ctx, `
		SELECT t.relname, c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE c.contype = 'f' AND t.relnamespace = 'public'::regnamespace
		ORDER BY t.relname, c.conname`)
	if err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	var out []foreignKey
	var fk foreignKey
	if _, err := pgx.ForEachRow(rows, []any{&fk.table, &fk.name, &fk.definition}, func() error {
		out = append(out, fk)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	return out, nil
}

// listForcedTables returns the public tables that force row-level security.
func listForcedTables(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT relname FROM pg_class
		WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r', 'p') AND relforcerowsecurity
		ORDER BY relname`)
	if err != nil {
		return nil, fmt.Errorf("list forced tables: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list forced tables: %w", err)
	}
	return names, nil
}

// qualified quotes a public object's name for SQL text.
func qualified(name string) string { return pgx.Identifier{"public", name}.Sanitize() }

// columnList quotes columns for a COPY or SELECT column list.
func columnList(columns []string) string {
	out := ""
	for i, c := range columns {
		if i > 0 {
			out += ", "
		}
		out += pgx.Identifier{c}.Sanitize()
	}
	return out
}
