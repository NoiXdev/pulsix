package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	gosql "github.com/go-sql-driver/mysql"
	"github.com/noixdev/pulsix/internal/containertest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// flavor is one server the plugin runs against. rootEnv is the variable
// that sets the image's root password; the replication statements differ
// between the two servers.
type flavor struct {
	name, image, rootEnv string
	// grants are the privileges the docs tell an operator to give the
	// monitoring user: the least that lets SHOW REPLICA STATUS run (found
	// by trying; SHOW GLOBAL STATUS and max_connections need none).
	// MariaDB's REPLICATION CLIENT no longer covers it since 10.5.9, and
	// SLAVE MONITOR is the older name of REPLICA MONITOR.
	grants        string
	pointAtSource string
	startReplica  string
	stopReplica   string
	resetReplica  string
	// namedSource, startNamed, stopNamed and resetNamed drive a named
	// replication connection (MariaDB multi-source); empty where the server
	// has none. Plain SHOW SLAVE STATUS shows only the default connection.
	namedSource, startNamed, stopNamed, resetNamed string
}

var flavors = []flavor{
	{
		name: "mysql", image: "mysql:8.4", rootEnv: "MYSQL_ROOT_PASSWORD",
		grants:        "REPLICATION CLIENT",
		pointAtSource: "CHANGE REPLICATION SOURCE TO SOURCE_HOST='127.0.0.1', SOURCE_PORT=1, SOURCE_USER='nobody', SOURCE_PASSWORD='x', SOURCE_CONNECT_RETRY=1",
		startReplica:  "START REPLICA", stopReplica: "STOP REPLICA", resetReplica: "RESET REPLICA ALL",
	},
	{
		name: "mariadb", image: "mariadb:11.4", rootEnv: "MARIADB_ROOT_PASSWORD",
		grants:        "REPLICA MONITOR",
		pointAtSource: "CHANGE MASTER TO MASTER_HOST='127.0.0.1', MASTER_PORT=1, MASTER_USER='nobody', MASTER_PASSWORD='x', MASTER_CONNECT_RETRY=1",
		startReplica:  "START SLAVE", stopReplica: "STOP SLAVE", resetReplica: "RESET SLAVE ALL",
		namedSource: "CHANGE MASTER 'named' TO MASTER_HOST='127.0.0.1', MASTER_PORT=1, MASTER_USER='nobody', MASTER_PASSWORD='x', MASTER_CONNECT_RETRY=1",
		startNamed:  "START SLAVE 'named'", stopNamed: "STOP SLAVE 'named'", resetNamed: "RESET SLAVE 'named' ALL",
	},
}

const rootPassword = "root-pw"

// startServer runs fl's image and returns the host and port, waiting until a
// root login works (the image's init phase restarts the server). opts
// adjust the request (files, server options).
func startServer(t *testing.T, fl flavor, opts ...func(*testcontainers.ContainerRequest)) (host, port string) {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        fl.image,
		ExposedPorts: []string{"3306/tcp"},
		Env:          map[string]string{fl.rootEnv: rootPassword},
		WaitingFor:   wait.ForListeningPort("3306/tcp").WithStartupTimeout(120 * time.Second),
	}
	for _, o := range opts {
		o(&req)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Fatalf("start %s: %v", fl.image, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	host, err = c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	port = mapped.Port()

	deadline := time.Now().Add(120 * time.Second)
	for {
		db, err := openAdmin(host, port)
		if err == nil {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = db.PingContext(pctx)
			cancel()
			_ = db.Close()
		}
		if err == nil {
			return host, port
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not accept a login: %v", fl.image, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func openAdmin(host, port string) (*sql.DB, error) {
	return openAs(host, port, "root", rootPassword)
}

func openAs(host, port, user, password string) (*sql.DB, error) {
	cfg := gosql.NewConfig()
	cfg.User, cfg.Passwd = user, password
	cfg.Net, cfg.Addr = "tcp", host+":"+port
	cfg.Timeout = 3 * time.Second
	cfg.Logger = &gosql.NopLogger{}
	c, err := gosql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}

// TestAgainstARealServer runs the plugin against MySQL 8.4 and MariaDB
// 11.4 with a dedicated monitoring user, the setup the docs tell an
// operator to make.
func TestAgainstARealServer(t *testing.T) {
	containertest.RequireLinuxDocker(t)
	for _, fl := range flavors {
		t.Run(fl.name, func(t *testing.T) {
			t.Parallel()
			host, port := startServer(t, fl)
			ctx := context.Background()
			admin, err := openAdmin(host, port)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = admin.Close() }()
			admin.SetMaxOpenConns(1) // one session: its SET GLOBAL and statements are ordered
			exec := func(q string) {
				t.Helper()
				if _, err := admin.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			exec(`CREATE USER 'monitor'@'%' IDENTIFIED BY 'monitor-pw'`)
			exec(`GRANT ` + fl.grants + ` ON *.* TO 'monitor'@'%'`)
			exec(`CREATE USER 'plain'@'%' IDENTIFIED BY 'plain-pw'`)
			exec(`GRANT USAGE ON *.* TO 'plain'@'%'`)
			// The admin session is already open, so it needs its own setting.
			exec(`SET SESSION long_query_time = 0.01`)

			fields := func(extra map[string]string) map[string]string {
				f := map[string]string{"host": host, "port": port, "user": "monitor", "password": "monitor-pw"}
				for k, v := range extra {
					f[k] = v
				}
				return f
			}
			p := New().(*plugin)

			t.Run("healthy primary", func(t *testing.T) {
				obs, err := p.CollectWithSecret(ctx, nil, fields(nil))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 1)
				conns, _ := find(obs, "", "mysql.connections")
				max, _ := find(obs, "", "mysql.max_connections")
				pct, _ := find(obs, "", "mysql.connections_pct")
				if conns.Value == nil || *conns.Value < 2 || max.Value == nil || *max.Value < 100 || pct.Value == nil || *pct.Value <= 0 {
					t.Errorf("connections %+v max %+v pct %+v", conns, max, pct)
				}
				mustLabel(t, obs, "", "mysql.replication_state", "none")
				mustValue(t, obs, "", "mysql.replication_lag_seconds", 0)
				if _, ok := find(obs, "", "mysql.slow_queries_per_s"); ok {
					t.Error("a slow-query rate in the first round")
				}
			})

			t.Run("schema sizes", func(t *testing.T) {
				exec(`CREATE DATABASE sized`)
				exec(`CREATE DATABASE empty_db`)
				exec(`CREATE TABLE sized.bulk (id INT PRIMARY KEY, pad VARCHAR(200))`)
				exec(`INSERT INTO sized.bulk VALUES (1, REPEAT('x', 200)), (2, REPEAT('y', 200))`)
				// The grant the docs name: table sizes become visible, data
				// does not.
				exec(`CREATE USER 'sizer'@'%' IDENTIFIED BY 'sizer-pw'`)
				exec(`GRANT ` + fl.grants + `, SHOW VIEW ON *.* TO 'sizer'@'%'`)
				on := map[string]any{"schema_sizes": true}

				obs, err := p.CollectWithSecret(ctx, on, fields(map[string]string{"user": "sizer", "password": "sizer-pw"}))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 1)
				if o, ok := find(obs, "sized", "mysql.schema_size_bytes"); !ok || o.Value == nil || *o.Value <= 0 {
					t.Errorf("sized = %+v", o)
				}
				mustValue(t, obs, "", "mysql.schemas_unreported", 0)
				mustValue(t, obs, "empty_db", "mysql.schema_size_bytes", 0)
				for _, sys := range []string{"mysql", "information_schema", "performance_schema", "sys"} {
					if _, ok := find(obs, sys, "mysql.schema_size_bytes"); ok {
						t.Errorf("system schema %s reported", sys)
					}
				}

				sizer, err := openAs(host, port, "sizer", "sizer-pw")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = sizer.Close() }()
				var n int
				if err := sizer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sized.bulk`).Scan(&n); err == nil {
					t.Error("SHOW VIEW lets the user read data")
				}

				// Without SHOW VIEW the tables are not visible: nothing to
				// report, and nothing fails.
				obs, err = p.CollectWithSecret(ctx, on, fields(nil))
				if err != nil {
					t.Fatal(err)
				}
				if o, ok := find(obs, "sized", "mysql.schema_size_bytes"); ok {
					t.Errorf("sized reported without a privilege on its tables: %+v", o)
				}
				mustValue(t, obs, "", "mysql.schemas_unreported", 0)
			})

			t.Run("the server stops a bounded statement itself", func(t *testing.T) {
				// MySQL refuses the MariaDB form as a syntax error, which moves
				// Schemas on to the hinted form; MariaDB takes it.
				_, err := admin.ExecContext(ctx, boundedSelect(dialectMariaDB, time.Second, "SELECT 1"))
				var me *gosql.MySQLError
				if fl.name == "mysql" {
					if !errors.As(err, &me) || me.Number != errSyntax {
						t.Fatalf("MariaDB form on MySQL: %v, want a syntax error", err)
					}
				} else if err != nil {
					t.Fatalf("MariaDB form on MariaDB: %v", err)
				}
				// The form the server takes ends a long statement long before
				// it would end on its own (and no client deadline is set).
				d := dialectMariaDB
				if fl.name == "mysql" {
					d = dialectMySQL
				}
				start := time.Now()
				var r any
				err = admin.QueryRowContext(ctx, boundedSelect(d, 300*time.Millisecond, "SELECT SLEEP(5)")).Scan(&r)
				t.Logf("bounded SLEEP(5): %v after %v", err, time.Since(start))
				if elapsed := time.Since(start); elapsed > 3*time.Second {
					t.Fatalf("the server ran the bounded statement for %v", elapsed)
				}
			})

			t.Run("every TLS mode the server can answer", func(t *testing.T) {
				for _, mode := range []string{"false", "preferred", "skip-verify"} {
					obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"tls": mode}))
					if err != nil {
						t.Fatal(err)
					}
					mustValue(t, obs, "", "mysql.up", 1)
				}
			})

			t.Run("slow query rate across two rounds of one instance", func(t *testing.T) {
				inst := New().(*plugin).NewInstance().(*plugin)
				first, err := inst.CollectWithSecret(ctx, nil, fields(nil))
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := find(first, "", "mysql.slow_queries_per_s"); ok {
					t.Fatal("rate in the first round")
				}
				for i := 0; i < 5; i++ {
					exec(`SELECT SLEEP(0.05)`)
				}
				time.Sleep(1100 * time.Millisecond)
				second, err := inst.CollectWithSecret(ctx, nil, fields(nil))
				if err != nil {
					t.Fatal(err)
				}
				o, ok := find(second, "", "mysql.slow_queries_per_s")
				if !ok || o.Value == nil {
					t.Fatalf("no slow_queries_per_s in %+v", second)
				}
				t.Logf("slow_queries_per_s = %v", *o.Value)
				if *o.Value <= 0 || *o.Value > 8 {
					t.Fatalf("slow_queries_per_s = %v", *o.Value)
				}
			})

			t.Run("wrong password", func(t *testing.T) {
				obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"password": "wrong-" + plantedPassword}))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 0)
				mustErr(t, obs, "", "mysql.connections", "mysql: authentication failed (error 1045)")
				noSecretIn(t, obs, err)
			})

			t.Run("unknown user", func(t *testing.T) {
				obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"user": plantedUser}))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 0)
				mustErr(t, obs, "", "mysql.connections", "mysql: authentication failed (error 1045)")
				noSecretIn(t, obs, err)
			})

			t.Run("a user without the monitoring grants", func(t *testing.T) {
				obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"user": "plain", "password": "plain-pw"}))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 0)
				o, _ := find(obs, "", "mysql.connections")
				t.Logf("a user with USAGE only: %q", o.Err)
				if o.Err != "mysql: permission denied (error 1227)" && o.Err != "mysql: permission denied (error 1142)" {
					t.Errorf("error = %q", o.Err)
				}
			})

			t.Run("TLS required but not verifiable", func(t *testing.T) {
				// The server's certificate is self-signed.
				obs, err := p.CollectWithSecret(ctx, nil, fields(map[string]string{"tls": "true"}))
				if err != nil {
					t.Fatal(err)
				}
				mustValue(t, obs, "", "mysql.up", 0)
				mustErr(t, obs, "", "mysql.connections", "mysql: TLS handshake failed")
			})

			t.Run("a replica whose source is unreachable", func(t *testing.T) {
				exec(fl.pointAtSource)
				exec(fl.startReplica)
				var state string
				deadline := time.Now().Add(30 * time.Second)
				for {
					o, err := p.CollectWithSecret(ctx, nil, fields(nil))
					if err != nil {
						t.Fatal(err)
					}
					mustValue(t, o, "", "mysql.up", 1)
					if s, ok := find(o, "", "mysql.replication_state"); ok && s.Label != nil {
						state = *s.Label
					}
					if state == "connecting" {
						lag, _ := find(o, "", "mysql.replication_lag_seconds")
						t.Logf("connecting: lag %+v err %q", lag.Value, lag.Err)
						noSecretIn(t, o, nil)
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("state %q after 30 s, want connecting", state)
					}
					time.Sleep(500 * time.Millisecond)
				}
				exec(fl.stopReplica)
				o, err := p.CollectWithSecret(ctx, nil, fields(nil))
				if err != nil {
					t.Fatal(err)
				}
				mustLabel(t, o, "", "mysql.replication_state", "stopped")
				mustErr(t, o, "", "mysql.replication_lag_seconds", "mysql: replication is not running")
				noSecretIn(t, o, nil)
				exec(fl.resetReplica)
				o, _ = p.CollectWithSecret(ctx, nil, fields(nil))
				mustLabel(t, o, "", "mysql.replication_state", "none")
			})

			t.Run("a named replication connection", func(t *testing.T) {
				if fl.namedSource == "" {
					t.Skip("no named connections on this server")
				}
				exec(fl.namedSource)
				exec(fl.startNamed)
				deadline := time.Now().Add(30 * time.Second)
				for {
					o, err := p.CollectWithSecret(ctx, nil, fields(nil))
					if err != nil {
						t.Fatal(err)
					}
					s, _ := find(o, "", "mysql.replication_state")
					if s.Label != nil && *s.Label == "none" {
						t.Fatal("a configured named connection reads as none")
					}
					if s.Label != nil && *s.Label == "connecting" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("state %+v after 30 s, want connecting", s.Label)
					}
					time.Sleep(500 * time.Millisecond)
				}
				exec(fl.stopNamed)
				o, _ := p.CollectWithSecret(ctx, nil, fields(nil))
				mustLabel(t, o, "", "mysql.replication_state", "stopped")
				mustErr(t, o, "", "mysql.replication_lag_seconds", "mysql: replication is not running")
				exec(fl.resetNamed)
				o, _ = p.CollectWithSecret(ctx, nil, fields(nil))
				mustLabel(t, o, "", "mysql.replication_state", "none")
			})
		})
	}
}
