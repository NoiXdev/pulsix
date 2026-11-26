package checkplugins

// mysqlPlugin: MySQL and MariaDB server health (backlog 3.10). Where the
// server is and how to log in is the host's knowledge: it lives in the
// secret named by the required secret parameter, never in the manager.
// schema_sizes (backlog 3.10b) switches on one size per schema, on an item
// named by the schema; it is off by default because the query behind it
// can be slow on a server with many tables. The plugin has no item
// parameter: it reports every schema the monitoring user can see.
func mysqlPlugin() Descriptor {
	secret := SecretParam()
	secret.Required = true
	return Descriptor{
		Name: "mysql",
		Runs: Agent,
		Params: []ParamSpec{
			secret,
			{Name: "schema_sizes", Type: TypeBool, Default: false},
		},
		MetricAsked: mysqlMetricAsked,
		Metrics: []MetricSpec{
			{Name: "mysql.up", Kind: Numeric},
			{Name: "mysql.connections", Kind: Numeric},
			{Name: "mysql.max_connections", Kind: Numeric},
			{Name: "mysql.connections_pct", Kind: Numeric},
			{Name: "mysql.replication_state", Kind: Discrete},
			{Name: "mysql.replication_lag_seconds", Kind: Numeric},
			{Name: "mysql.slow_queries_per_s", Kind: Numeric},
			// With schema_sizes only. Item: a schema; and on item "" the
			// schemas left out (beyond the agent's cap, or a name the item
			// contract refuses), or the error of the sizes query.
			{Name: "mysql.schema_size_bytes", Kind: Numeric},
			{Name: "mysql.schemas_unreported", Kind: Numeric},
		},
	}
}

// mysqlMetricAsked: the schema metrics are reported only while
// schema_sizes is true — the agent's own reading of the parameter (a bool
// true, nothing else); every other metric is asked.
func mysqlMetricAsked(params map[string]any, metric string) bool {
	if metric != "mysql.schema_size_bytes" && metric != "mysql.schemas_unreported" {
		return true
	}
	on, _ := params["schema_sizes"].(bool)
	return on
}
