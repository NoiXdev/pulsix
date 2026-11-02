package backup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db"
)

// metricsTable is the one table IncludeMetrics switches.
const metricsTable = "check_samples"

// DumpOptions describes the backup Dump writes.
type DumpOptions struct {
	Kind           string // "scheduled" or "manual"
	PulsixVersion  string
	IncludeMetrics bool
	Now            func() time.Time // nil means time.Now
}

// Dump writes a complete backup of pool's database to w: the plaintext
// header, then every public table's rows and every sequence's state,
// encrypted under masterKey.
//
// Everything is read in one REPEATABLE READ READ ONLY transaction, so the
// backup is one consistent snapshot. pool may be the schema owner or
// pulsix_app without an organization scope: every forced-RLS table carries
// the org_isolation_bypass policy, which lets an unscoped session read all
// organizations' rows.
func Dump(ctx context.Context, pool *pgxpool.Pool, masterKey [32]byte, w io.Writer, opts DumpOptions) (Header, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Header{}, fmt.Errorf("dump: begin snapshot: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // read only; nothing to keep

	version, err := db.ReadSchemaVersion(ctx, tx)
	if err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	tables, err := listTables(ctx, tx)
	if err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	sequences, err := readSequences(ctx, tx)
	if err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}

	h := Header{
		FormatVersion:   FormatVersion,
		CreatedAt:       now().UTC(),
		Kind:            opts.Kind,
		PulsixVersion:   opts.PulsixVersion,
		SchemaVersion:   version,
		IncludesMetrics: opts.IncludeMetrics,
	}
	if err := WriteHeader(w, h); err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	enc, err := Encrypt(w, masterKey)
	if err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	fw := NewFrameWriter(enc)

	m := Manifest{Header: h, Sequences: sequences, Tables: []TableCount{}}
	for _, t := range tables {
		if t.name == metricsTable && !opts.IncludeMetrics {
			continue
		}
		rows, err := dumpTable(ctx, tx, fw, t)
		if err != nil {
			return Header{}, fmt.Errorf("dump: %w", err)
		}
		m.Tables = append(m.Tables, TableCount{Name: t.name, Rows: rows})
	}
	if err := fw.Manifest(m); err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	if err := enc.Close(); err != nil {
		return Header{}, fmt.Errorf("dump: %w", err)
	}
	return h, nil
}

// dumpTable copies one table's rows into a table frame in COPY text format.
// The query form matters for hypertables: a plain COPY of one returns only
// its empty parent, the rows live in its chunks.
func dumpTable(ctx context.Context, tx pgx.Tx, fw *FrameWriter, t tableInfo) (int64, error) {
	if err := fw.Begin(t.name, t.columns); err != nil {
		return 0, err
	}
	// COPY sends one message per row; batching them saves a frame header a
	// row.
	buf := bufio.NewWriterSize(fw, maxDataChunk)
	tag, err := tx.Conn().PgConn().CopyTo(ctx, buf,
		"COPY (SELECT "+columnList(t.columns)+" FROM "+qualified(t.name)+") TO STDOUT")
	if err != nil {
		return 0, fmt.Errorf("copy %s: %w", t.name, err)
	}
	if err := buf.Flush(); err != nil {
		return 0, fmt.Errorf("copy %s: %w", t.name, err)
	}
	rows := tag.RowsAffected()
	if err := fw.End(rows); err != nil {
		return 0, err
	}
	return rows, nil
}
