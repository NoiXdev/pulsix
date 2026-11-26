package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// replicaChannel is one replication channel of SHOW REPLICA STATUS. Only
// the thread states and the lag are read: the error and host columns can
// quote statements, data and credentials' owners, so they are never read.
type replicaChannel struct {
	// Name is the channel name; read only to skip Group Replication's.
	Name    string
	IO, SQL string
	// Lag is nil when the server reports NULL.
	Lag *float64
}

// serverStats is what the queries tell about the server. Replicas is empty
// on a server that is not a replica.
type serverStats struct {
	Connections, MaxConnections int64
	SlowQueries                 uint64
	// Uptime is the server's seconds since start; a fall means a restart.
	Uptime   uint64
	Replicas []replicaChannel
}

// session is one open connection. The plugin never sees the driver; tests
// fake this.
type session interface {
	Server(ctx context.Context) (serverStats, error)
	// Schemas reads the per-schema sizes (schemas.go).
	Schemas(ctx context.Context) (sizes []schemaSize, total int, err error)
	Close()
}

type mysqlSession struct {
	db       *sql.DB
	conn     *sql.Conn
	deadline time.Time // the round's deadline, zero when it has none
	// queryTimeout bounds each statement; zero means dbcheck.QueryTimeout.
	queryTimeout time.Duration
}

// queryCtx returns a context for one statement. The driver watches it: when
// it ends, the driver closes the connection and returns its own invalid
// connection error, so fail turns that back into the deadline.
func (s *mysqlSession) queryCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := s.queryTimeout
	if d <= 0 {
		d = dbcheck.QueryTimeout
	}
	return context.WithTimeout(ctx, d)
}

// fail reports a statement's own deadline as context.DeadlineExceeded
// ("timed out"), whatever error the driver gave when it closed the
// connection under it. Errors while the parent is alive and the deadline has
// not passed are returned unchanged.
func fail(parent, qctx context.Context, err error) error {
	if err != nil && parent.Err() == nil && errors.Is(qctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// connect opens one connection under ctx's deadline.
func connect(ctx context.Context, s connSettings) (session, error) {
	c, err := mysql.NewConnector(buildConfig(s))
	if err != nil {
		return nil, errInvalidSettings
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	sess, err := newSession(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	sess.queryTimeout = s.queryTimeout
	return sess, nil
}

// newSession takes the one connection of db.
func newSession(ctx context.Context, db *sql.DB) (*mysqlSession, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	d, _ := ctx.Deadline()
	return &mysqlSession{db: db, conn: conn, deadline: d}, nil
}

const (
	closeFloor = 100 * time.Millisecond
	closeCap   = 2 * time.Second

	statusQuery   = `SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected','Slow_queries','Uptime')`
	maxConnsQuery = `SELECT @@GLOBAL.max_connections`

	// The statements that read replication, newest first. The ALL forms are
	// MariaDB's: plain SHOW REPLICA/SLAVE STATUS shows only its default
	// connection, so a broken named one would read as no replication.
	allReplicasQuery = `SHOW ALL REPLICAS STATUS` // MariaDB 10.5.1+
	replicaQuery     = `SHOW REPLICA STATUS`      // MySQL 8.0.22+
	allSlavesQuery   = `SHOW ALL SLAVES STATUS`   // older MariaDB
	slaveQuery       = `SHOW SLAVE STATUS`        // older MySQL

	// errSyntax is the server error for a statement it does not know: SHOW
	// REPLICA STATUS on MySQL before 8.0.22.
	errSyntax = 1064
)

func (s *mysqlSession) Server(ctx context.Context) (serverStats, error) {
	var st serverStats
	if err := s.status(ctx, &st); err != nil {
		return serverStats{}, err
	}
	qctx, cancel := s.queryCtx(ctx)
	err := s.conn.QueryRowContext(qctx, maxConnsQuery).Scan(&st.MaxConnections)
	err = fail(ctx, qctx, err)
	cancel()
	if err != nil {
		return serverStats{}, err
	}
	if st.Replicas, err = s.replicas(ctx); err != nil {
		return serverStats{}, err
	}
	return st, nil
}

func (s *mysqlSession) status(parent context.Context, st *serverStats) (err error) {
	ctx, cancel := s.queryCtx(parent)
	defer cancel()
	defer func() { err = fail(parent, ctx, err) }()
	rows, err := s.conn.QueryContext(ctx, statusQuery)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var haveConns, haveSlow, haveUptime bool
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		n, perr := strconv.ParseUint(value, 10, 63)
		if perr != nil {
			return errUnexpectedResult
		}
		switch name {
		case "Threads_connected":
			st.Connections, haveConns = int64(n), true
		case "Slow_queries":
			st.SlowQueries, haveSlow = n, true
		case "Uptime":
			st.Uptime, haveUptime = n, true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !haveConns || !haveSlow || !haveUptime {
		return errUnexpectedResult
	}
	return nil
}

// replicas reads the replication status with the newest statement the
// server knows: each syntax error (1064) moves to the next of the chain.
func (s *mysqlSession) replicas(parent context.Context) (out []replicaChannel, err error) {
	ctx, cancel := s.queryCtx(parent)
	defer cancel()
	defer func() { err = fail(parent, ctx, err) }()
	var rows *sql.Rows
	for _, q := range []string{allReplicasQuery, replicaQuery, allSlavesQuery, slaveQuery} {
		rows, err = s.conn.QueryContext(ctx, q)
		var me *mysql.MySQLError
		if !errors.As(err, &me) || me.Number != errSyntax {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		ch, err := channelFromRow(cols, vals)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// channelFromRow reads the thread states and the lag from one status row by
// column name, accepting both the current (Replica_*, Seconds_Behind_Source)
// and the legacy (Slave_*, Seconds_Behind_Master) spellings. A NULL lag, or
// one that is not a number, is nil.
func channelFromRow(cols []string, vals []sql.RawBytes) (replicaChannel, error) {
	var ch replicaChannel
	var haveIO, haveSQL bool
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "replica_io_running", "slave_io_running":
			ch.IO, haveIO = string(vals[i]), true
		case "replica_sql_running", "slave_sql_running":
			ch.SQL, haveSQL = string(vals[i]), true
		case "channel_name":
			ch.Name = string(vals[i])
		case "seconds_behind_source", "seconds_behind_master":
			if vals[i] != nil {
				if f, err := strconv.ParseFloat(string(vals[i]), 64); err == nil && f >= 0 {
					ch.Lag = &f
				}
			}
		}
	}
	if !haveIO || !haveSQL {
		return replicaChannel{}, errUnexpectedResult
	}
	return ch, nil
}

// Close ends the connection within what remains of the round's deadline,
// but never less than closeFloor (the round's own may be spent) and never
// more than closeCap, so a hung server cannot stretch the round. A close
// that is still blocked is abandoned to finish on its own.
func (s *mysqlSession) Close() {
	wait := closeCap
	if !s.deadline.IsZero() {
		wait = min(max(time.Until(s.deadline), closeFloor), closeCap)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.conn.Close()
		_ = s.db.Close()
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}
