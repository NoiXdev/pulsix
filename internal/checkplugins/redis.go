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
		},
	}
}
