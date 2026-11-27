package workers

import (
	"context"
	"errors"
	"fmt"
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
type SQLRevocationFeed struct{ pool *pgxpool.Pool }

// NewSQLRevocationFeed builds the feed on pool.
func NewSQLRevocationFeed(pool *pgxpool.Pool) *SQLRevocationFeed {
	return &SQLRevocationFeed{pool: pool}
}

// Listen takes a connection and subscribes it to RevocationChannel.
func (f *SQLRevocationFeed) Listen(ctx context.Context) (RevocationSession, error) {
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a connection: %w", err)
	}
	s := &sqlRevocationSession{conn: conn}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{RevocationChannel}.Sanitize()); err != nil {
		s.Close()
		return nil, fmt.Errorf("listen on %s: %w", RevocationChannel, err)
	}
	return s, nil
}

// sqlRevocationSession is one LISTENing connection.
type sqlRevocationSession struct {
	conn  *pgxpool.Conn
	close sync.Once
}

// Next waits for a notification. The wait ending is not an error: the
// connection survives a read deadline, and the caller rescans. A payload
// that is not a worker id reads as uuid.Nil, which rescans as well.
func (s *sqlRevocationSession) Next(ctx context.Context, wait time.Duration) (uuid.UUID, error) {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	n, err := s.conn.Conn().WaitForNotification(wctx)
	if err != nil {
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) && !s.conn.Conn().IsClosed() {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	id, err := uuid.Parse(n.Payload)
	if err != nil {
		return uuid.Nil, nil
	}
	return id, nil
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
