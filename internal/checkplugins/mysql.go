package checkplugins

// mysqlPlugin: MySQL and MariaDB server health (backlog 3.10). Where the
// server is and how to log in is the host's knowledge: it lives in the
// secret named by the required secret parameter, never in the manager. The
// plugin reports the server as a whole, so it has no item parameter.
func mysqlPlugin() Descriptor {
	secret := SecretParam()
	secret.Required = true
	return Descriptor{
		Name:   "mysql",
		Runs:   Agent,
		Params: []ParamSpec{secret},
		Metrics: []MetricSpec{
			{Name: "mysql.up", Kind: Numeric},
			{Name: "mysql.connections", Kind: Numeric},
			{Name: "mysql.max_connections", Kind: Numeric},
			{Name: "mysql.connections_pct", Kind: Numeric},
			{Name: "mysql.replication_state", Kind: Discrete},
			{Name: "mysql.replication_lag_seconds", Kind: Numeric},
			{Name: "mysql.slow_queries_per_s", Kind: Numeric},
		},
	}
}
