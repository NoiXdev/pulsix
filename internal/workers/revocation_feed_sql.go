package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// sessionCloseTimeout bounds closing a revocation session's connection.
const sessionCloseTimeout = 5 * time.Second

// SQLRevocationFeed is the RevocationFeed on Postgres LISTEN. Each session
// holds one pooled connection for as long as it lives, so the manager
// gives it the pool its advisory lockers hold their sessions on (backlog
// 3.92), never the one request work runs on.
type SQLRevocationFeed struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewSQLRevocationFeed builds the feed on pool; a nil log is slog.Default.
func NewSQLRevocationFeed(pool *pgxpool.Pool, log *slog.Logger) *SQLRevocationFeed {
	if log == nil {
		log = slog.Default()
	}
	return &SQLRevocationFeed{pool: pool, log: log}
}

// Listen takes a connection and subscribes it to RevocationChannel.
func (f *SQLRevocationFeed) Listen(ctx context.Context) (RevocationSession, error) {
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a connection: %w", err)
	}
	s := &sqlRevocationSession{conn: conn, log: f.log}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{RevocationChannel}.Sanitize()); err != nil {
		s.Close()
		return nil, fmt.Errorf("listen on %s: %w", RevocationChannel, err)
	}
	return s, nil
}

// sqlRevocationSession is one LISTENing connection.
type sqlRevocationSession struct {
	conn  *pgxpool.Conn
	log   *slog.Logger
	close sync.Once
}

// Next waits for a notification. The wait ending is not an error: the
// connection survives a read deadline, and the caller decides whether to
// rescan. A payload that is not a worker id is skipped and the wait goes
// on: only Pulsix announces on the channel, so such a payload carries
// nothing, and rescanning for it would let anyone who can NOTIFY make
// every manager query (backlog 2.54 review M2).
func (s *sqlRevocationSession) Next(ctx context.Context, wait time.Duration) (uuid.UUID, error) {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		n, err := s.conn.Conn().WaitForNotification(wctx)
		if err != nil {
			if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) && !s.conn.Conn().IsClosed() {
				return uuid.Nil, nil
			}
			return uuid.Nil, err
		}
		if id, err := uuid.Parse(n.Payload); err == nil {
			return id, nil
		}
		s.log.DebugContext(ctx, "ignored a worker revocation notification that names no worker", "payload_bytes", len(n.Payload))
	}
}

// NotActive runs ListWorkersNotActive on the session's own connection.
func (s *sqlRevocationSession) NotActive(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	return gen.New(s.conn).ListWorkersNotActive(ctx, ids)
}

// Close closes the connection rather than returning it to the pool: it is
// still subscribed, and it may be broken.
func (s *sqlRevocationSession) Close() {
	s.close.Do(func() {
		raw := s.conn.Hijack()
		ctx, cancel := context.WithTimeout(context.Background(), sessionCloseTimeout)
		defer cancel()
		_ = raw.Close(ctx)
	})
}
