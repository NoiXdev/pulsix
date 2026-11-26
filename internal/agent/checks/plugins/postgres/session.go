package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// serverStats is what one query tells about the server. Lags and the
// oldest transaction are 0 when they do not apply.
type serverStats struct {
	Connections, MaxConnections int64
	InRecovery                  bool
	LagSeconds, LagBytes        float64
	OldestXactSeconds           float64
	// LimitedView is true when the user lacks pg_read_all_stats: other
	// roles' sessions then show no backend type or transaction start, so
	// Connections and OldestXactSeconds would read silently low.
	LimitedView bool
}

// database is one row of the size listing; Size is nil when the user may
// not read it.
type database struct {
	Name string
	Size *int64
}

// session is one open connection. The plugin never sees pgx; tests fake
// this.
type session interface {
	Server(ctx context.Context) (serverStats, error)
	// Databases lists at most limit databases: the named ones when names is
	// not empty, otherwise every non-template database that accepts
	// connections. Ordered by name.
	Databases(ctx context.Context, names []string, limit int) (dbs []database, total int, err error)
	// Replicas lists at most limit of the WAL senders this server runs,
	// without pg_basebackup's, and how many there are in all.
	Replicas(ctx context.Context, limit int) (rs []replica, total int, err error)
	Close()
}

// pgxSession is the production session.
type pgxSession struct {
	conn     *pgx.Conn
	deadline time.Time // the round's deadline, zero when it has none
}

const (
	closeFloor = 100 * time.Millisecond
	closeCap   = 2 * time.Second
)

// connect opens one connection under ctx's deadline.
func connect(ctx context.Context, s connSettings) (session, error) {
	cfg, err := buildConfig(s)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	d, _ := ctx.Deadline()
	return pgxSession{conn: conn, deadline: d}, nil
}

// serverQuery reads everything about the server in one statement. The lag
// is measured in the standby's replay: 0 when everything received is
// replayed, else the time since the last replayed transaction. On a primary
// both are 0. The oldest transaction counts client backends only, other
// than this one (autovacuum and other workers are not what a rule on
// "stuck transaction" is after). The last column is true for a user without
// pg_read_all_stats, who cannot see other roles' sessions properly: the plugin
// then reports the two session-based metrics as errors, not as low numbers.
const serverQuery = `SELECT
  (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend')::bigint,
  current_setting('max_connections')::bigint,
  pg_is_in_recovery(),
  (CASE WHEN pg_is_in_recovery() THEN
     CASE WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
          ELSE GREATEST(COALESCE(EXTRACT(EPOCH FROM now() - pg_last_xact_replay_timestamp()), 0), 0)
     END
   ELSE 0 END)::float8,
  (CASE WHEN pg_is_in_recovery() THEN
     COALESCE(GREATEST(pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 0), 0)
   ELSE 0 END)::float8,
  COALESCE((SELECT EXTRACT(EPOCH FROM now() - min(xact_start))
              FROM pg_stat_activity
             WHERE pid <> pg_backend_pid() AND backend_type = 'client backend' AND xact_start IS NOT NULL), 0)::float8,
  NOT pg_has_role('pg_read_all_stats', 'USAGE')`

func (s pgxSession) Server(ctx context.Context) (serverStats, error) {
	var st serverStats
	err := s.conn.QueryRow(ctx, serverQuery).Scan(
		&st.Connections, &st.MaxConnections, &st.InRecovery, &st.LagSeconds, &st.LagBytes, &st.OldestXactSeconds, &st.LimitedView)
	return st, err
}

const (
	allDatabasesQuery = `SELECT datname::text,
  CASE WHEN has_database_privilege(datname, 'CONNECT') OR pg_has_role('pg_read_all_stats', 'USAGE') THEN pg_database_size(datname) END,
  count(*) OVER ()
  FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname LIMIT $1`
	namedDatabasesQuery = `SELECT datname::text,
  CASE WHEN has_database_privilege(datname, 'CONNECT') OR pg_has_role('pg_read_all_stats', 'USAGE') THEN pg_database_size(datname) END,
  count(*) OVER ()
  FROM pg_database WHERE datname = ANY($2::text[]) ORDER BY datname LIMIT $1`
)

// Databases reads at most limit rows and reports in total how many the query
// matches without its LIMIT (count(*) OVER (), the same statement, so the
// two cannot disagree).
func (s pgxSession) Databases(ctx context.Context, names []string, limit int) ([]database, int, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if len(names) > 0 {
		rows, err = s.conn.Query(ctx, namedDatabasesQuery, limit, names)
	} else {
		rows, err = s.conn.Query(ctx, allDatabasesQuery, limit)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var (
		out   []database
		total int
	)
	for rows.Next() {
		var d database
		if err := rows.Scan(&d.Name, &d.Size, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// Close ends the connection within what remains of the round's deadline,
// but never less than closeFloor (the round's own may be spent) and never
// more than closeCap, so a hung server cannot stretch the round.
func (s pgxSession) Close() {
	wait := closeCap
	if !s.deadline.IsZero() {
		wait = min(max(time.Until(s.deadline), closeFloor), closeCap)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_ = s.conn.Close(ctx)
}
