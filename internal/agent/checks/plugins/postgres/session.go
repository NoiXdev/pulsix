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
	Databases(ctx context.Context, names []string, limit int) ([]database, error)
	Close()
}

// pgxSession is the production session.
type pgxSession struct{ conn *pgx.Conn }

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
	return pgxSession{conn: conn}, nil
}

// serverQuery reads everything about the server in one statement. The lag
// is measured in the standby's replay: 0 when everything received is
// replayed, else the time since the last replayed transaction. On a primary
// both are 0. The oldest transaction counts client backends only, other
// than this one (autovacuum and other workers are not what a rule on
// "stuck transaction" is after); a user without pg_monitor sees no
// transaction start of other users' sessions and reads 0.
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
             WHERE pid <> pg_backend_pid() AND backend_type = 'client backend' AND xact_start IS NOT NULL), 0)::float8`

func (s pgxSession) Server(ctx context.Context) (serverStats, error) {
	var st serverStats
	err := s.conn.QueryRow(ctx, serverQuery).Scan(
		&st.Connections, &st.MaxConnections, &st.InRecovery, &st.LagSeconds, &st.LagBytes, &st.OldestXactSeconds)
	return st, err
}

const (
	allDatabasesQuery = `SELECT datname::text,
  CASE WHEN has_database_privilege(datname, 'CONNECT') OR pg_has_role('pg_read_all_stats', 'USAGE') THEN pg_database_size(datname) END
  FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname LIMIT $1`
	namedDatabasesQuery = `SELECT datname::text,
  CASE WHEN has_database_privilege(datname, 'CONNECT') OR pg_has_role('pg_read_all_stats', 'USAGE') THEN pg_database_size(datname) END
  FROM pg_database WHERE datname = ANY($2::text[]) ORDER BY datname LIMIT $1`
)

func (s pgxSession) Databases(ctx context.Context, names []string, limit int) ([]database, error) {
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
		return nil, err
	}
	defer rows.Close()
	var out []database
	for rows.Next() {
		var d database
		if err := rows.Scan(&d.Name, &d.Size); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Close ends the connection with a short, fresh deadline: the round's own
// may be spent, and a hung server must not hold the close.
func (s pgxSession) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.conn.Close(ctx)
}
