package redis

import (
	"errors"
	"strconv"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// persistenceMetrics are the INFO persistence metrics (backlog 3.10b), all
// on item "" and all reported on every successful round.
var persistenceMetrics = []string{
	"redis.rdb_last_bgsave_status", "redis.rdb_last_save_age_seconds", "redis.rdb_changes_since_last_save",
	"redis.aof_enabled", "redis.aof_last_write_status", "redis.aof_last_bgrewrite_status", "redis.loading",
}

// persistence reports the snapshot and append-only-file state. Each metric
// stands on its own field: one that is missing or unreadable is an error on
// that metric, never a zero or an "ok". The AOF statuses are reported with
// AOF off too (Redis keeps them "ok" then). now is the agent's clock; the
// server's own (server_time_usec, Redis 6.2+) measures the save age when it
// is there, so a skewed clock between agent and server does not.
func persistence(kv map[string]string, now time.Time) []checks.Observation {
	obs := make([]checks.Observation, 0, len(persistenceMetrics))
	obs = append(obs, okErr(kv, "rdb_last_bgsave_status", "redis.rdb_last_bgsave_status"))

	if saved, err := strconv.ParseInt(kv["rdb_last_save_time"], 10, 64); err == nil {
		at := now.Unix()
		if usec, ok := uintField(kv, "server_time_usec"); ok {
			at = int64(usec / 1_000_000)
		}
		obs = append(obs, checks.ValueObservation("", "redis.rdb_last_save_age_seconds", float64(max(at-saved, 0))))
	} else {
		obs = append(obs, checks.ErrObservation("", "redis.rdb_last_save_age_seconds", notReported("rdb_last_save_time")))
	}

	if n, ok := uintField(kv, "rdb_changes_since_last_save"); ok {
		obs = append(obs, checks.ValueObservation("", "redis.rdb_changes_since_last_save", float64(n)))
	} else {
		obs = append(obs, checks.ErrObservation("", "redis.rdb_changes_since_last_save", notReported("rdb_changes_since_last_save")))
	}

	return append(obs,
		zeroOne(kv, "aof_enabled", "redis.aof_enabled"),
		okErr(kv, "aof_last_write_status", "redis.aof_last_write_status"),
		okErr(kv, "aof_last_bgrewrite_status", "redis.aof_last_bgrewrite_status"),
		zeroOne(kv, "loading", "redis.loading"),
	)
}

// okErr reports a status field that Redis writes as "ok" or "err" as that
// label.
func okErr(kv map[string]string, field, metric string) checks.Observation {
	v, ok := kv[field]
	switch {
	case !ok:
		return checks.ErrObservation("", metric, notReported(field))
	case v == "ok" || v == "err":
		return checks.LabelObservation("", metric, v)
	}
	return checks.ErrObservation("", metric, errors.New("redis: "+field+" is not ok or err"))
}

// zeroOne reports a flag field that Redis writes as 0 or 1 as that value.
func zeroOne(kv map[string]string, field, metric string) checks.Observation {
	v, ok := kv[field]
	switch {
	case !ok:
		return checks.ErrObservation("", metric, notReported(field))
	case v == "0" || v == "1":
		return checks.ValueObservation("", metric, map[string]float64{"0": 0, "1": 1}[v])
	}
	return checks.ErrObservation("", metric, errors.New("redis: "+field+" is not 0 or 1"))
}
