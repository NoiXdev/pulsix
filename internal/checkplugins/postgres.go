package checkplugins

// Bounds of the postgres plugin's databases list (backlog 3.10). The agent
// trusts params that pass here, so these and internal/agent/checks/plugins/
// postgres agree: a PostgreSQL identifier is at most 63 bytes.
const (
	maxPostgresDatabases     = 64
	maxPostgresDatabaseBytes = 63
)

// postgresPlugin: PostgreSQL server health (backlog 3.10). Where the server
// is and how to log in is the host's knowledge: it lives in the secret
// named by the required secret parameter, never in the manager. databases
// optionally limits the size metric to the listed databases; the server's
// own item "" is asked whatever the list says.
func postgresPlugin() Descriptor {
	secret := SecretParam()
	secret.Required = true
	return Descriptor{
		Name: "postgres",
		Runs: Agent,
		Params: []ParamSpec{
			secret,
			{Name: "databases", Type: TypeList, MaxEntries: maxPostgresDatabases, MaxBytes: maxPostgresDatabaseBytes, Format: FormatNoControl},
		},
		ItemParam:           "databases",
		HostItemAlwaysAsked: true,
		Metrics: []MetricSpec{
			{Name: "pg.up", Kind: Numeric},
			{Name: "pg.connections", Kind: Numeric},
			{Name: "pg.max_connections", Kind: Numeric},
			{Name: "pg.connections_pct", Kind: Numeric},
			{Name: "pg.in_recovery", Kind: Numeric},
			{Name: "pg.replication_lag_seconds", Kind: Numeric},
			{Name: "pg.replication_lag_bytes", Kind: Numeric},
			{Name: "pg.oldest_xact_seconds", Kind: Numeric},
			{Name: "pg.database_size_bytes", Kind: Numeric},
		},
	}
}
