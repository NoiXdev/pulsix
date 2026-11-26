package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

const (
	metricReplicas = "pg.replicas"
	// replicaItemPrefix starts every replica item; the manager asks for such
	// items whatever the databases list says.
	replicaItemPrefix = checkplugins.PostgresReplicaItemPrefix
	// maxReplicas is how many replicas one round reads and reports;
	// pg.replicas counts them all.
	maxReplicas = 64
)

// replica is one WAL sender of pg_stat_replication. LagBytes is nil when the
// replica has reported no replay position; LagSeconds when PostgreSQL has
// measured no lag time (it clears them once a replica is caught up and
// idle).
type replica struct {
	Name, Host, State    string
	LagBytes, LagSeconds *float64
}

// replicasQuery lists the WAL senders that are replicas: physical standbys
// and logical subscriptions' apply senders. Left out are pg_basebackup's
// senders (its base backup, state backup, and its -X stream WAL sender),
// WAL archivers that never replay (pg_receivewal, barman_receive_wal),
// logical table-sync workers, named after their slot (PostgreSQL 14+
// pg_<subid>_sync_<relid>_<sysid>, 10 to 13 <slot>_<subid>_sync_<relid>),
// and a sender that has reported no replay position yet: a standby that
// just connected counts from its first reply.
//
// The byte lag is measured from what this server has (on a cascading
// standby what it received, else what it replayed) to the replica's replay
// position, never below 0. The time lag is the first of the replay, flush
// and write lag the server measured; NULL when it measured none.
// Ordered so that a cap keeps a stable set.
const replicasQuery = `SELECT COALESCE(application_name, '')::text,
  COALESCE(host(client_addr), 'local'),
  COALESCE(state::text, ''),
  GREATEST(pg_wal_lsn_diff(
    CASE WHEN pg_is_in_recovery() THEN COALESCE(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn())
         ELSE pg_current_wal_lsn() END, replay_lsn), 0)::float8,
  EXTRACT(EPOCH FROM COALESCE(replay_lag, flush_lag, write_lag))::float8,
  count(*) OVER ()
  FROM pg_stat_replication
 WHERE state IS DISTINCT FROM 'backup'
   AND replay_lsn IS NOT NULL
   AND COALESCE(application_name, '') NOT IN ('pg_basebackup', 'pg_receivewal', 'barman_receive_wal')
   AND COALESCE(application_name, '') !~ '_[0-9]+_sync_[0-9]+(_[0-9]+)?$'
 ORDER BY 1, 2, 4 DESC NULLS LAST
 LIMIT $1`

// Replicas reads at most limit senders and reports in total how many there
// are.
func (s pgxSession) Replicas(ctx context.Context, limit int) ([]replica, int, error) {
	rows, err := s.conn.Query(ctx, replicasQuery, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var (
		out   []replica
		total int
	)
	for rows.Next() {
		var r replica
		if err := rows.Scan(&r.Name, &r.Host, &r.State, &r.LagBytes, &r.LagSeconds, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// replicaItem is a replica's item: its name and host, or its host alone
// when it sends no name.
func replicaItem(r replica) string {
	if r.Name == "" {
		return replicaItemPrefix + r.Host
	}
	return replicaItemPrefix + r.Name + "@" + r.Host
}

// worse reports whether a is further behind than b: a known byte lag beats
// an unknown one, a larger one a smaller one.
func worse(a, b replica) bool {
	switch {
	case a.LagBytes == nil:
		return false
	case b.LagBytes == nil:
		return true
	}
	return *a.LagBytes > *b.LagBytes
}

// observeReplicas reports the count on item "" and each replica on its
// item. Senders that share an item (the same name and host) report the one
// furthest behind. limited is a user without pg_read_all_stats, who sees no
// sender's details, so nothing but an error is reported; so is readErr, the
// classified failure of the replica read (reason), which leaves the rest of
// the round as it is.
func observeReplicas(rs []replica, total int, limited bool, readErr string) ([]checks.Observation, error) {
	if limited {
		return []checks.Observation{checks.ErrObservation("", metricReplicas, errors.New("postgres: no privilege to see replication"))}, nil
	}
	if readErr != "" {
		return []checks.Observation{checks.ErrObservation("", metricReplicas, errors.New("postgres: "+readErr))}, nil
	}
	obs := []checks.Observation{checks.ValueObservation("", metricReplicas, float64(total))}
	var errs []error
	byItem := make(map[string]int, len(rs))
	var order []string
	keep := make([]replica, 0, len(rs))
	for _, r := range rs {
		item := replicaItem(r)
		if !checks.ReportableItem(item) {
			errs = append(errs, fmt.Errorf("postgres: replica %q cannot be reported: name longer than %d bytes, not valid UTF-8 or carrying a control character", item, checks.MaxItemLen))
			continue
		}
		i, seen := byItem[item]
		if !seen {
			byItem[item] = len(keep)
			order = append(order, item)
			keep = append(keep, r)
			continue
		}
		if worse(r, keep[i]) {
			keep[i] = r
		}
	}
	for _, item := range order {
		obs = append(obs, replicaObservations(item, keep[byItem[item]])...)
	}
	return obs, errors.Join(errs...)
}

// replicaObservations: the byte lag, the time lag and the state. An
// unknown byte lag is an error, never 0. The time lag is 0 while nothing is
// behind, else the lag time the server measured; with bytes behind and no
// lag time yet (the window after an idle spell, until the replica's next
// reply) it is left out of the round rather than guessed or failed.
func replicaObservations(item string, r replica) []checks.Observation {
	obs := make([]checks.Observation, 0, 3)
	if r.LagBytes != nil {
		obs = append(obs, checks.ValueObservation(item, "pg.replica_lag_bytes", *r.LagBytes))
	} else {
		obs = append(obs, checks.ErrObservation(item, "pg.replica_lag_bytes", errors.New("postgres: replica reports no replay position")))
	}
	switch {
	case r.LagBytes != nil && *r.LagBytes == 0:
		obs = append(obs, checks.ValueObservation(item, "pg.replica_lag_seconds", 0))
	case r.LagSeconds != nil:
		obs = append(obs, checks.ValueObservation(item, "pg.replica_lag_seconds", max(*r.LagSeconds, 0)))
	}
	return append(obs, checks.LabelObservation(item, "pg.replica_state", r.State))
}
