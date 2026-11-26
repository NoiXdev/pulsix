package checkplugins

// redisPlugin: Redis and Valkey (and other Redis-protocol servers) health
// (backlog 3.10). Where the server is and how to log in is the host's
// knowledge: it lives in the secret named by the secret parameter, never in
// the manager. Unlike postgres and mysql the secret is optional: a check
// without one reads 127.0.0.1:6379 without authentication. The plugin
// reports the server as a whole, so it has no item parameter.
func redisPlugin() Descriptor {
	return Descriptor{
		Name:   "redis",
		Runs:   Agent,
		Params: []ParamSpec{SecretParam()},
		Metrics: []MetricSpec{
			{Name: "redis.up", Kind: Numeric},
			{Name: "redis.memory_used_bytes", Kind: Numeric},
			{Name: "redis.memory_max_bytes", Kind: Numeric},
			{Name: "redis.memory_pct", Kind: Numeric},
			{Name: "redis.clients", Kind: Numeric},
			{Name: "redis.role", Kind: Discrete},
			{Name: "redis.replication_link", Kind: Discrete},
			{Name: "redis.replication_lag_seconds", Kind: Numeric},
			{Name: "redis.evicted_keys_per_s", Kind: Numeric},
			// Persistence (backlog 3.10b): the last snapshot's and the
			// append-only file's write and rewrite status as Redis writes them
			// (ok, err), the seconds since the last successful snapshot (since
			// start when there was none), the changes not yet in one, whether
			// AOF is on and whether the server is loading its data (0/1).
			{Name: "redis.rdb_last_bgsave_status", Kind: Discrete},
			{Name: "redis.rdb_last_save_age_seconds", Kind: Numeric},
			{Name: "redis.rdb_changes_since_last_save", Kind: Numeric},
			{Name: "redis.aof_enabled", Kind: Numeric},
			{Name: "redis.aof_last_write_status", Kind: Discrete},
			{Name: "redis.aof_last_bgrewrite_status", Kind: Discrete},
			{Name: "redis.loading", Kind: Numeric},
		},
	}
}
