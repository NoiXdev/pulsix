package mysql

import (
	"context"
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/agent/checks"
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
// left out (backlog 3.10b). It sees only the tables the user holds some
// privilege on (SHOW VIEW is enough, and reads no data). MySQL 8 answers
// from its statistics cache (information_schema_stats_expiry); MariaDB
// opens every table, which is why the plugin asks only with schema_sizes.
// The LIMIT is maxSchemas; COUNT(*) OVER () counts every schema.
const schemasQuery = `SELECT TABLE_SCHEMA, CAST(SUM(COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0)) AS UNSIGNED), COUNT(*) OVER () ` +
	`FROM information_schema.TABLES ` +
	`WHERE TABLE_SCHEMA NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys') ` +
	`GROUP BY TABLE_SCHEMA ORDER BY TABLE_SCHEMA LIMIT 256`

// Schemas reads at most maxSchemas schema sizes and how many schemas there
// are in all.
func (s *mysqlSession) Schemas(parent context.Context) (out []schemaSize, total int, err error) {
	ctx, cancel := s.queryCtx(parent)
	defer cancel()
	defer func() { err = fail(parent, ctx, err) }()
	rows, err := s.conn.QueryContext(ctx, schemasQuery)
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
