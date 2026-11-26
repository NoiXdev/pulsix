package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
)

func i64(v int64) *int64 { return &v }

func TestIsASecretPlugin(t *testing.T) {
	sp, ok := New().(checks.SecretPlugin)
	if !ok {
		t.Fatal("New() is not a checks.SecretPlugin")
	}
	if sp.Name() != "postgres" || sp.SecretErrorMetric() != "pg.up" {
		t.Fatalf("name %q, metric %q", sp.Name(), sp.SecretErrorMetric())
	}
}

func TestPrimaryMetrics(t *testing.T) {
	sess := &fakeSession{
		stats: serverStats{Connections: 12, MaxConnections: 100, OldestXactSeconds: 0},
		dbs:   []database{{Name: "app", Size: i64(2048)}, {Name: "postgres", Size: i64(8000000)}},
	}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 1)
	mustValue(t, obs, "", "pg.connections", 12)
	mustValue(t, obs, "", "pg.max_connections", 100)
	mustValue(t, obs, "", "pg.connections_pct", 12)
	mustValue(t, obs, "", "pg.in_recovery", 0)
	mustValue(t, obs, "", "pg.replication_lag_seconds", 0)
	mustValue(t, obs, "", "pg.replication_lag_bytes", 0)
	mustValue(t, obs, "", "pg.oldest_xact_seconds", 0)
	mustValue(t, obs, "", "pg.databases_unreported", 0)
	mustValue(t, obs, "app", "pg.database_size_bytes", 2048)
	mustValue(t, obs, "postgres", "pg.database_size_bytes", 8000000)
	if !sess.closed {
		t.Error("session not closed")
	}
	if len(obs) != 10+2 {
		t.Errorf("%d observations: %+v", len(obs), obs)
	}
}

func TestStandbyAndTransactions(t *testing.T) {
	tests := []struct {
		name  string
		stats serverStats
		want  map[string]float64
	}{
		{"standby caught up", serverStats{Connections: 1, MaxConnections: 4, InRecovery: true}, map[string]float64{"pg.in_recovery": 1, "pg.replication_lag_seconds": 0, "pg.replication_lag_bytes": 0, "pg.connections_pct": 25}},
		{"standby lagging", serverStats{Connections: 1, MaxConnections: 4, InRecovery: true, LagSeconds: 42.5, LagBytes: 1 << 20}, map[string]float64{"pg.in_recovery": 1, "pg.replication_lag_seconds": 42.5, "pg.replication_lag_bytes": 1 << 20}},
		{"oldest xact some", serverStats{Connections: 1, MaxConnections: 4, OldestXactSeconds: 3600}, map[string]float64{"pg.oldest_xact_seconds": 3600, "pg.in_recovery": 0}},
		{"oldest xact none", serverStats{Connections: 1, MaxConnections: 4}, map[string]float64{"pg.oldest_xact_seconds": 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := collect(t, newTestPlugin(&fakeSession{stats: tt.stats}, nil, nil), nil, goodFields)
			if err != nil {
				t.Fatal(err)
			}
			for m, v := range tt.want {
				mustValue(t, obs, "", m, v)
			}
			for _, m := range hostMetrics {
				if _, ok := find(obs, "", m); !ok {
					t.Errorf("%s stranded", m)
				}
			}
		})
	}
}

func TestDatabasesFilterIsPassedToTheSession(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: []database{{Name: "app", Size: i64(1)}}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), map[string]any{"databases": []any{"app", "gone"}}, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sess.gotNames, ",") != "app,gone" {
		t.Errorf("names = %v", sess.gotNames)
	}
	mustValue(t, obs, "app", "pg.database_size_bytes", 1)
	// A listed database the server does not have reports its own error.
	mustErr(t, obs, "gone", "pg.database_size_bytes", "postgres: database does not exist")
}

func TestNoFilterPassesNoNames(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}}
	if _, err := collect(t, newTestPlugin(sess, nil, nil), map[string]any{"databases": []any{}}, goodFields); err != nil {
		t.Fatal(err)
	}
	if len(sess.gotNames) != 0 {
		t.Errorf("names = %v", sess.gotNames)
	}
}

func TestDatabaseCap(t *testing.T) {
	var dbs []database
	for i := 0; i < 300; i++ {
		dbs = append(dbs, database{Name: fmt.Sprintf("db%03d", i), Size: i64(int64(i))})
	}
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: dbs}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	n := 0
	for _, o := range obs {
		if o.Metric == "pg.database_size_bytes" {
			n++
		}
	}
	if n != maxDatabases {
		t.Fatalf("%d databases reported, want %d", n, maxDatabases)
	}
	if _, ok := find(obs, "db256", "pg.database_size_bytes"); ok {
		t.Error("database over the cap reported")
	}
	if err == nil || !strings.Contains(err.Error(), "256") || !strings.Contains(err.Error(), `"db256"`) {
		t.Errorf("advisory error does not name the skipped databases: %v", err)
	}
	mustValue(t, obs, "", "pg.databases_unreported", 44)
	if sess.gotLimit <= maxDatabases || sess.gotLimit > 1024 {
		t.Errorf("row limit %d is not bounded just above the cap", sess.gotLimit)
	}
}

func TestDatabaseWithoutSizePrivilege(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: []database{{Name: "secret_db"}, {Name: "ok", Size: i64(5)}}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustErr(t, obs, "secret_db", "pg.database_size_bytes", "postgres: no privilege to read the size")
	mustValue(t, obs, "ok", "pg.database_size_bytes", 5)
	mustValue(t, obs, "", "pg.up", 1)
}

func TestUnreportableDatabaseNameIsSkippedAndNamed(t *testing.T) {
	for _, name := range []string{"we\x00b", "a\nb", "q\xffz", strings.Repeat("n", 600)} {
		sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: []database{{Name: name, Size: i64(1)}, {Name: "ok", Size: i64(2)}}}
		obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
		if err == nil || !strings.Contains(err.Error(), "cannot be reported") {
			t.Errorf("%q: err = %v", name, err)
		}
		for _, o := range obs {
			if !checks.ReportableItem(o.Item) {
				t.Errorf("%q: unreportable item %q", name, o.Item)
			}
		}
		mustValue(t, obs, "ok", "pg.database_size_bytes", 2)
		mustValue(t, obs, "", "pg.databases_unreported", 1)
	}
}

// Refused names and databases beyond the cap are both counted.
func TestUnreportedCountsRefusedNamesAndCappedDatabases(t *testing.T) {
	dbs := []database{{Name: "a\nb", Size: i64(1)}}
	for i := 0; i < maxDatabases+3; i++ {
		dbs = append(dbs, database{Name: fmt.Sprintf("db%03d", i), Size: i64(1)})
	}
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: dbs}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	mustValue(t, obs, "", "pg.databases_unreported", 4)
}

func TestFieldsReachTheConnection(t *testing.T) {
	var got connSettings
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{"host": "/var/run/postgresql", "port": "5433", "user": "u", "password": "p", "database": "d", "sslmode": "require", "sslrootcert": ca}
	if _, err := collect(t, newTestPlugin(sess, nil, &got), nil, fields); err != nil {
		t.Fatal(err)
	}
	want := connSettings{host: "/var/run/postgresql", port: "5433", user: "u", password: "p", database: "d", sslmode: "require", sslrootcert: ca}
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
	got = connSettings{}
	if _, err := collect(t, newTestPlugin(sess, nil, &got), nil, map[string]string{"user": "u"}); err != nil {
		t.Fatal(err)
	}
	want = connSettings{host: "localhost", port: "5432", user: "u", database: "postgres", sslmode: "prefer"}
	if got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
}

func TestBadSecretFields(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"no user", map[string]string{"password": plantedPassword}, "postgres: secret field `user` is required"},
		{"bad port", map[string]string{"user": "u", "port": "99999"}, "postgres: secret field `port` is not a valid port"},
		{"bad sslmode", map[string]string{"user": "u", "sslmode": plantedPassword}, "postgres: secret field `sslmode` is not one of disable, prefer, require, verify-ca, verify-full"},
		{"unknown", map[string]string{"user": "u", "pasword": plantedPassword}, "postgres: secret field `pasword` is not known"},
		{"host with comma", map[string]string{"user": "u", "host": plantedHost + ",b"}, "postgres: secret field `host` is not a single host name or a socket directory"},
		{"NUL in password", map[string]string{"user": "u", "password": "a\x00b"}, "postgres: secret field `password` is not text without NUL bytes"},
		{"relative sslrootcert", map[string]string{"user": "u", "sslrootcert": plantedHost + ".pem"}, "postgres: secret field `sslrootcert` is not an absolute path or system"},
		{"relative sslcert", map[string]string{"user": "u", "sslcert": plantedHost + ".pem", "sslkey": "/k"}, "postgres: secret field `sslcert` is not an absolute path"},
		{"relative sslkey", map[string]string{"user": "u", "sslcert": "/c", "sslkey": plantedHost + ".key"}, "postgres: secret field `sslkey` is not an absolute path"},
		{"sslcert without TLS", map[string]string{"user": "u", "sslmode": "disable", "sslcert": "/c", "sslkey": "/k"}, "postgres: secret field `sslcert` needs `sslmode` other than disable"},
		{"sslkey without TLS", map[string]string{"user": "u", "sslmode": "disable", "sslkey": "/k"}, "postgres: secret field `sslkey` needs `sslmode` other than disable"},
		{"sslcert without sslkey", map[string]string{"user": "u", "sslcert": "/" + plantedHost + ".pem"}, "postgres: secret field `sslcert` needs `sslkey`"},
		{"sslkey without sslcert", map[string]string{"user": "u", "sslkey": "/" + plantedHost + ".key"}, "postgres: secret field `sslkey` needs `sslcert`"},
		{"sslcert over a socket", map[string]string{"user": "u", "host": "/var/run/postgresql", "sslcert": "/c", "sslkey": "/k"}, "postgres: secret field `sslcert` needs a TCP host, not a socket directory"},
		{"sslkey over a socket", map[string]string{"user": "u", "host": "/var/run/postgresql", "sslkey": "/k"}, "postgres: secret field `sslkey` needs a TCP host, not a socket directory"},
		{"unreadable sslcert", map[string]string{"user": "u", "sslcert": "/" + plantedHost + ".pem", "sslkey": "/k"}, "postgres: secret field `sslcert` cannot be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &fakeSession{}
			obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, tt.fields)
			if len(obs) != 1 {
				t.Fatalf("obs = %+v", obs)
			}
			mustErr(t, obs, "", "pg.up", tt.want)
			noSecretIn(t, obs, err)
		})
	}
}

func TestUnreadableRootCertIsNamedWithoutThePath(t *testing.T) {
	f := map[string]string{"user": "u", "sslrootcert": "/nonexistent/" + plantedHost + ".pem"}
	obs, err := collect(t, newTestPlugin(&fakeSession{}, nil, nil), nil, f)
	if len(obs) != 1 {
		t.Fatalf("obs = %+v", obs)
	}
	mustErr(t, obs, "", "pg.up", "postgres: secret field `sslrootcert` cannot be read")
	noSecretIn(t, obs, err)
}

// A client certificate reaches the connection settings and the key file
// is checked before anything dials.
func TestClientCertificateFields(t *testing.T) {
	dir := t.TempDir()
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	cert := testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644)
	key := testpki.Write(t, dir, "c.key", client.KeyPEM, 0o600)
	var got connSettings
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}}
	if _, err := collect(t, newTestPlugin(sess, nil, &got), nil, map[string]string{"user": "u", "sslmode": "verify-full", "sslcert": cert, "sslkey": key}); err != nil {
		t.Fatal(err)
	}
	if got.sslcert != cert || got.sslkey != key {
		t.Fatalf("settings = %+v", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	open := testpki.Write(t, dir, plantedHost+".key", client.KeyPEM, 0o644)
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, map[string]string{"user": "u", "sslcert": cert, "sslkey": open})
	mustErr(t, obs, "", "pg.up", "postgres: secret field `sslkey` is accessible to other users; remove their permissions, for example chmod 600")
	noSecretIn(t, obs, err)
}

// sslrootcert "system" means the system roots: no file is read, and the
// value reaches the connection as it is.
func TestRootCertSystem(t *testing.T) {
	var got connSettings
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}}
	obs, err := collect(t, newTestPlugin(sess, nil, &got), nil, map[string]string{"user": "u", "sslmode": "verify-full", "sslrootcert": "system"})
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 1)
	if got.sslrootcert != "system" {
		t.Fatalf("settings = %+v", got)
	}
	cfg, err := buildConfig(got)
	if err != nil || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.RootCAs == nil {
		t.Fatalf("system roots not configured: %+v, %v", cfg, err)
	}
}

// pgx reads only the key file's first block, so leading EC PARAMETERS
// (openssl ecparam -genkey) are named instead of failing inside pgx.
func TestKeyWithECParametersIsNamed(t *testing.T) {
	dir := t.TempDir()
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	der, err := x509.MarshalECPrivateKey(client.Key)
	if err != nil {
		t.Fatal(err)
	}
	oid := []byte{0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07} // prime256v1
	key := append(pem.EncodeToMemory(&pem.Block{Type: "EC PARAMETERS", Bytes: oid}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})...)
	f := map[string]string{
		"user": "u", "sslcert": testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644),
		"sslkey": testpki.Write(t, dir, "c.key", key, 0o600),
	}
	obs, _ := collect(t, newTestPlugin(&fakeSession{}, nil, nil), nil, f)
	mustErr(t, obs, "", "pg.up", "postgres: secret field `sslkey` does not start with the private key; remove the PEM blocks before it (such as EC PARAMETERS)")
}

func TestNilFieldsAreAConfigurationError(t *testing.T) {
	obs, err := newTestPlugin(&fakeSession{}, nil, nil).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("obs = %+v", obs)
	}
	mustErr(t, obs, "", "pg.up", "postgres: no secret given")
}

func TestFailureClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"auth", fmt.Errorf("failed to connect to user=%s host=%s: %w", plantedUser, plantedHost, pgErr("28P01", plantedPassword)), "authentication failed (SQLSTATE 28P01)"},
		{"no hba entry", pgErr("28000", plantedHost), "authentication failed (SQLSTATE 28000)"},
		{"permission", pgErr("42501", plantedUser), "permission denied (SQLSTATE 42501)"},
		{"other code", pgErr("53300", "too many"), "server error (SQLSTATE 53300)"},
		{"hostile code", pgErr("28P01\nrm -rf", "x"), "server error"},
		{"timeout", fmt.Errorf("x %s: %w", plantedHost, context.DeadlineExceeded), "timed out"},
		{"statement timeout", pgErr("57014", "canceling statement due to statement timeout"), "timed out"},
		{"tls refused", errors.New("server refused TLS connection to " + plantedHost), "TLS handshake failed"},
		{"other", errors.New("dial " + plantedHost + " " + plantedPassword), "connection failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &fakeSession{}
			obs, err := collect(t, newTestPlugin(sess, tt.err, nil), nil, goodFields)
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "", "pg.up", 0)
			for _, m := range hostMetrics {
				mustErr(t, obs, "", m, "postgres: "+tt.want)
			}
			if len(obs) != 1+len(hostMetrics) {
				t.Errorf("obs = %+v", obs)
			}
			noSecretIn(t, obs, err)
		})
	}
}

func TestQueryFailuresAreDownToo(t *testing.T) {
	for name, sess := range map[string]*fakeSession{
		"server":    {statsErr: pgErr("42501", "x")},
		"databases": {dbsErr: pgErr("57014", "canceled")},
	} {
		t.Run(name, func(t *testing.T) {
			obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
			if err != nil {
				t.Fatal(err)
			}
			mustValue(t, obs, "", "pg.up", 0)
			if len(obs) != 1+len(hostMetrics) {
				t.Errorf("obs = %+v", obs)
			}
			if !sess.closed {
				t.Error("session not closed")
			}
			for _, o := range obs {
				if o.Item != "" {
					t.Errorf("item %q reported while down", o.Item)
				}
			}
		})
	}
}

func TestCallerContextEndingReturnsOnlyItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := plugin{connect: func(ctx context.Context, _ connSettings) (session, error) {
		cancel()
		return nil, ctx.Err()
	}}
	obs, err := p.CollectWithSecret(ctx, nil, goodFields)
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("obs = %+v, err = %v", obs, err)
	}
	obs, err = p.CollectWithSecret(ctx, nil, goodFields)
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: obs = %+v, err = %v", obs, err)
	}
}

func TestOwnDeadlineIsTimedOut(t *testing.T) {
	p := plugin{timeout: 50 * time.Millisecond, connect: func(ctx context.Context, _ connSettings) (session, error) {
		<-ctx.Done()
		return nil, errors.New("something unrelated " + plantedHost)
	}}
	obs, err := p.CollectWithSecret(context.Background(), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 0)
	mustErr(t, obs, "", "pg.connections", "postgres: timed out")
	noSecretIn(t, obs, err)
}

func TestBadDatabasesParam(t *testing.T) {
	obs, _ := collect(t, newTestPlugin(&fakeSession{}, nil, nil), map[string]any{"databases": "app"}, goodFields)
	if len(obs) != 1 || obs[0].Metric != "pg.up" || obs[0].Err == "" {
		t.Fatalf("obs = %+v", obs)
	}
}

func TestZeroMaxConnectionsDoesNotDivide(t *testing.T) {
	obs, _ := collect(t, newTestPlugin(&fakeSession{stats: serverStats{Connections: 3}}, nil, nil), nil, goodFields)
	mustErr(t, obs, "", "pg.connections_pct", "postgres: max_connections is not positive")
}

func TestNotACAFileIsNamedWithoutThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), plantedHost+".pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	obs, err := collect(t, newTestPlugin(&fakeSession{}, nil, nil), nil, map[string]string{"user": "u", "sslrootcert": path})
	if len(obs) != 1 {
		t.Fatalf("obs = %+v", obs)
	}
	mustErr(t, obs, "", "pg.up", "postgres: secret field `sslrootcert` is not a CA file")
	noSecretIn(t, obs, err)
}

// Without pg_read_all_stats other roles' sessions are invisible, so the
// connection count and the oldest transaction would read low without saying so.
func TestLimitedViewReportsTheSessionMetricsAsErrors(t *testing.T) {
	sess := &fakeSession{stats: serverStats{Connections: 3, MaxConnections: 100, LimitedView: true}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 1)
	mustValue(t, obs, "", "pg.max_connections", 100)
	mustValue(t, obs, "", "pg.in_recovery", 0)
	for _, m := range []string{"pg.connections", "pg.connections_pct", "pg.oldest_xact_seconds"} {
		mustErr(t, obs, "", m, "postgres: no privilege to see other sessions")
	}
}

// testCAPEM returns a self-signed CA certificate in PEM.
func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The count is exact beyond the rows the query reads: the server's own total
// minus what was reported.
func TestUnreportedIsExactBeyondTheReadWindow(t *testing.T) {
	var dbs []database
	for i := 0; i < databaseRowLimit; i++ {
		dbs = append(dbs, database{Name: fmt.Sprintf("db%04d", i), Size: i64(1)})
	}
	total := 1000
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbs: dbs, total: &total}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	mustValue(t, obs, "", "pg.databases_unreported", float64(1000-maxDatabases))
}

func TestFailedRoundsFailTheUnreportedCountToo(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 1}, dbsErr: errors.New("boom")}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if o, ok := find(obs, "", "pg.databases_unreported"); !ok || o.Err == "" {
		t.Fatalf("pg.databases_unreported on a failed round = %+v, %v", o, ok)
	}
}
