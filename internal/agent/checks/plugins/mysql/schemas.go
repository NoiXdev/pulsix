package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

const (
	metricSchemaSize        = "mysql.schema_size_bytes"
	metricSchemasUnreported = "mysql.schemas_unreported"
	paramSchemaSizes        = "schema_sizes"
	maxSchemas              = 256 // the LIMIT of schemasQuery
)

// schemaSize is one schema's tables, data and indexes, in bytes.
type schemaSize struct {
	Name  string
	Bytes int64
}

// schemasQuery sums each schema's data and index length, system schemas
// left out (backlog 3.10b). It starts from SCHEMATA, so a schema without
// tables reports 0. It sees only the schemas and tables the user holds some
// privilege on (SHOW VIEW is enough, and reads no data); a login that sees a
// schema but none of its tables reads 0 for it. MySQL 8 answers from its
// statistics cache (information_schema_stats_expiry); MariaDB opens every
// table, which is why the plugin asks only with schema_sizes, and why
// Schemas sends it under a server-side bound (boundedSelect). The LIMIT is
// maxSchemas; COUNT(*) OVER () counts every schema.
const schemasQuery = `SELECT s.SCHEMA_NAME, CAST(COALESCE(SUM(COALESCE(t.DATA_LENGTH, 0) + COALESCE(t.INDEX_LENGTH, 0)), 0) AS UNSIGNED), COUNT(*) OVER () ` +
	`FROM information_schema.SCHEMATA s LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME ` +
	`WHERE s.SCHEMA_NAME NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys') ` +
	`GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME LIMIT 256`

// dialect is the SQL a statement bound is written in.
type dialect int

const (
	dialectMariaDB dialect = iota
	dialectMySQL
)

// boundedSelect makes the server itself stop query (a SELECT) after bound,
// so a slow sizes query does not keep running once the client gave up on
// it: MariaDB's SET STATEMENT max_statement_time (seconds) or MySQL's
// MAX_EXECUTION_TIME optimizer hint (milliseconds, directly after SELECT).
// MySQL refuses the first as a syntax error; MariaDB would read the hint as
// a plain comment, which is why the MariaDB form is tried first.
func boundedSelect(d dialect, bound time.Duration, query string) string {
	if d == dialectMariaDB {
		return fmt.Sprintf("SET STATEMENT max_statement_time=%.3f FOR %s", bound.Seconds(), query)
	}
	return fmt.Sprintf("SELECT /*+ MAX_EXECUTION_TIME(%d) */ %s", bound.Milliseconds(), strings.TrimPrefix(query, "SELECT "))
}

// serverBound is the server-side limit for a statement whose client
// deadline is client (zero: dbcheck.QueryTimeout): a tenth below it, so the
// server's own timeout normally answers first, and never under 1 ms (0
// would mean no limit).
func serverBound(client time.Duration) time.Duration {
	if client <= 0 {
		client = dbcheck.QueryTimeout
	}
	return max((client - client/10).Truncate(time.Millisecond), time.Millisecond)
}

// Schemas reads at most maxSchemas schema sizes and how many schemas there
// are in all, under a server-side bound in the server's dialect, found the
// way the replication chain finds its statement: a syntax error (1064) on
// the MariaDB form moves to the MySQL one.
func (s *mysqlSession) Schemas(parent context.Context) (out []schemaSize, total int, err error) {
	ctx, cancel := s.queryCtx(parent)
	defer cancel()
	defer func() { err = fail(parent, ctx, err) }()
	bound := serverBound(s.queryTimeout)
	var rows *sql.Rows
	for _, d := range []dialect{dialectMariaDB, dialectMySQL} {
		rows, err = s.conn.QueryContext(ctx, boundedSelect(d, bound, schemasQuery))
		var me *mysql.MySQLError
		if !errors.As(err, &me) || me.Number != errSyntax {
			break
		}
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sz schemaSize
		if err := rows.Scan(&sz.Name, &sz.Bytes, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, sz)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// schemaSizesAsked reads the schema_sizes parameter: only true asks.
func schemaSizesAsked(params map[string]any) bool {
	on, _ := params[paramSchemaSizes].(bool)
	return on
}

// observeSchemas reports each schema's size and, on item "", how many
// schemas were left out: those beyond the cap and those whose name the item
// contract refuses. A failed query (a timeout on a server with many tables,
// a missing privilege) is an error on that count alone; the round stays up.
func observeSchemas(sizes []schemaSize, total int, err error) ([]checks.Observation, error) {
	if err != nil {
		return []checks.Observation{checks.ErrObservation("", metricSchemasUnreported, errors.New("mysql: "+classify(err)))}, nil
	}
	obs := make([]checks.Observation, 0, len(sizes)+1)
	var errs []error
	reported := 0
	for _, sz := range sizes {
		if !checks.ReportableItem(sz.Name) {
			errs = append(errs, fmt.Errorf("mysql: schema %q cannot be reported: name longer than %d bytes, not valid UTF-8 or carrying a control character", sz.Name, checks.MaxItemLen))
			continue
		}
		reported++
		obs = append(obs, checks.ValueObservation(sz.Name, metricSchemaSize, float64(sz.Bytes)))
	}
	obs = append(obs, checks.ValueObservation("", metricSchemasUnreported, float64(max(total-reported, 0))))
	return obs, errors.Join(errs...)
}
