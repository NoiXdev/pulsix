package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// errNoTLSForTest is the driver's refusal when TLS is required and the
// server has none.
var errNoTLSForTest = mysql.ErrNoTLS

// fakeResult is the scripted answer to one statement. A nil value is NULL.
type fakeResult struct {
	cols []string
	rows [][]any
	err  error
}

// fakeDB is a database/sql driver answering from a script, recording the
// statements it saw.
type fakeDB struct {
	mu      sync.Mutex
	results map[string]fakeResult
	seen    []string
}

func (f *fakeDB) Connect(context.Context) (driver.Conn, error) { return &fakeConn{db: f}, nil }
func (f *fakeDB) Driver() driver.Driver                        { return nil }

type fakeConn struct{ db *fakeDB }

func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*fakeConn) Close() error                        { return nil }
func (*fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.seen = append(c.db.seen, query)
	r, ok := c.db.results[query]
	if !ok {
		return nil, errors.New("unscripted statement " + query)
	}
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{r: r}, nil
}

type fakeRows struct {
	r fakeResult
	i int
}

func (f *fakeRows) Columns() []string { return f.r.cols }
func (f *fakeRows) Close() error      { return nil }
func (f *fakeRows) Next(dest []driver.Value) error {
	if f.i >= len(f.r.rows) {
		return io.EOF
	}
	for i, v := range f.r.rows[f.i] {
		switch x := v.(type) {
		case nil:
			dest[i] = nil
		case string:
			dest[i] = []byte(x)
		case int:
			dest[i] = int64(x)
		default:
			dest[i] = x
		}
	}
	f.i++
	return nil
}

func statusResult(conns, slow string) fakeResult {
	return fakeResult{cols: []string{"Variable_name", "Value"}, rows: [][]any{
		{"Slow_queries", slow}, {"Threads_connected", conns}, {"Uptime", "5000"},
	}}
}

func newFakeSession(t *testing.T, results map[string]fakeResult) (*mysqlSession, *fakeDB) {
	t.Helper()
	if _, ok := results[statusQuery]; !ok {
		results[statusQuery] = statusResult("7", "42")
	}
	if _, ok := results[maxConnsQuery]; !ok {
		results[maxConnsQuery] = fakeResult{cols: []string{"@@GLOBAL.max_connections"}, rows: [][]any{{"151"}}}
	}
	f := &fakeDB{results: results}
	db := sql.OpenDB(f)
	db.SetMaxOpenConns(1)
	s, err := newSession(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, f
}

var (
	newCols    = []string{"Replica_IO_State", "Source_Host", "Last_IO_Error", "Replica_IO_Running", "Replica_SQL_Running", "Seconds_Behind_Source", "Last_SQL_Error", "Channel_Name"}
	legacyCols = []string{"Slave_IO_State", "Master_Host", "Last_IO_Error", "Slave_IO_Running", "Slave_SQL_Running", "Seconds_Behind_Master", "Last_SQL_Error", "Channel_Name"}
)

// replicaRow is a status row with the sensitive columns filled, which the
// plugin must not read.
func replicaRow(io, sqlr string, lag any, channel string) []any {
	return []any{"Waiting", plantedHost, plantedPassword, io, sqlr, lag, plantedUser, channel}
}

func TestServerReadsStatusAndNoReplication(t *testing.T) {
	s, f := newFakeSession(t, map[string]fakeResult{allReplicasQuery: {cols: newCols}})
	st, err := s.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Connections != 7 || st.MaxConnections != 151 || st.SlowQueries != 42 || st.Uptime != 5000 || len(st.Replicas) != 0 {
		t.Fatalf("stats %+v", st)
	}
	if slices.Contains(f.seen, slaveQuery) {
		t.Fatal("fell back without an error")
	}
}

func TestReplicaStatusBothSpellings(t *testing.T) {
	for name, cols := range map[string][]string{"replica": newCols, "legacy": legacyCols} {
		t.Run(name, func(t *testing.T) {
			s, _ := newFakeSession(t, map[string]fakeResult{allReplicasQuery: {cols: cols, rows: [][]any{
				replicaRow("Yes", "Yes", "12", "a"),
			}}})
			st, err := s.Server(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Replicas) != 1 {
				t.Fatalf("replicas %+v", st.Replicas)
			}
			ch := st.Replicas[0]
			if ch.IO != "Yes" || ch.SQL != "Yes" || ch.Lag == nil || *ch.Lag != 12 {
				t.Fatalf("channel %+v", ch)
			}
		})
	}
}

func TestReplicaStatusNullAndMultipleChannels(t *testing.T) {
	s, _ := newFakeSession(t, map[string]fakeResult{allReplicasQuery: {cols: newCols, rows: [][]any{
		replicaRow("Yes", "Yes", "3", "a"),
		replicaRow("Yes", "No", nil, "b"),
	}}})
	st, err := s.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Replicas) != 2 || st.Replicas[0].Lag == nil || st.Replicas[1].Lag != nil || st.Replicas[1].SQL != "No" {
		t.Fatalf("replicas %+v", st.Replicas)
	}
	state, lag, lagErr := replication(st.Replicas)
	if state != "stopped" || lag != 3 || lagErr != "mysql: replication is not running" {
		t.Fatalf("state %q lag %v err %q", state, lag, lagErr)
	}
}

func TestFallbackToShowSlaveStatusOnSyntaxError(t *testing.T) {
	s, f := newFakeSession(t, map[string]fakeResult{
		allReplicasQuery: {err: &mysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}},
		replicaQuery:     {err: &mysql.MySQLError{Number: 1064}},
		allSlavesQuery:   {err: &mysql.MySQLError{Number: 1064}},
		slaveQuery:       {cols: legacyCols, rows: [][]any{replicaRow("Yes", "Yes", "0", "")}},
	})
	st, err := s.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Replicas) != 1 {
		t.Fatalf("replicas %+v", st.Replicas)
	}
	want := []string{statusQuery, maxConnsQuery, allReplicasQuery, replicaQuery, allSlavesQuery, slaveQuery}
	if !slices.Equal(f.seen, want) {
		t.Fatalf("statements %v, want %v", f.seen, want)
	}
}

// Each step of the chain is the one a given server knows: MySQL 8.0.22+
// answers the second, older MariaDB the third.
func TestFallbackChainStopsAtTheFirstAnswer(t *testing.T) {
	syntax := fakeResult{err: &mysql.MySQLError{Number: 1064}}
	ok := fakeResult{cols: legacyCols, rows: [][]any{replicaRow("Yes", "Yes", "0", "")}}
	tests := []struct {
		name    string
		results map[string]fakeResult
		last    string
	}{
		{"mariadb 10.5.1+", map[string]fakeResult{allReplicasQuery: ok}, allReplicasQuery},
		{"mysql 8.0.22+", map[string]fakeResult{allReplicasQuery: syntax, replicaQuery: ok}, replicaQuery},
		{"older mariadb", map[string]fakeResult{allReplicasQuery: syntax, replicaQuery: syntax, allSlavesQuery: ok}, allSlavesQuery},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFakeSession(t, tc.results)
			st, err := s.Server(context.Background())
			if err != nil || len(st.Replicas) != 1 {
				t.Fatalf("replicas %+v, err %v", st.Replicas, err)
			}
			if f.seen[len(f.seen)-1] != tc.last {
				t.Fatalf("statements %v, want the last to be %s", f.seen, tc.last)
			}
		})
	}
}

// The connection name of MariaDB's multi-source output is never read; the
// channel name only to skip Group Replication's internal channels.
func TestChannelNamesAreReadForGroupReplicationOnly(t *testing.T) {
	cols := append(slices.Clone(legacyCols), "Connection_name")
	row := append(replicaRow("Yes", "Yes", "1", "group_replication_applier"), plantedUser)
	s, _ := newFakeSession(t, map[string]fakeResult{allReplicasQuery: {cols: cols, rows: [][]any{row}}})
	st, err := s.Server(context.Background())
	if err != nil || len(st.Replicas) != 1 || st.Replicas[0].Name != "group_replication_applier" {
		t.Fatalf("%+v %v", st.Replicas, err)
	}
	noSecretIn(t, observe(st, 0, false), nil)
}

func TestNoFallbackOnOtherErrors(t *testing.T) {
	s, f := newFakeSession(t, map[string]fakeResult{
		allReplicasQuery: {err: &mysql.MySQLError{Number: 1227, Message: "Access denied"}},
	})
	_, err := s.Server(context.Background())
	if got := classify(err); got != "permission denied (error 1227)" {
		t.Fatalf("classify = %q (err %v)", got, err)
	}
	if slices.Contains(f.seen, replicaQuery) || slices.Contains(f.seen, slaveQuery) {
		t.Fatal("fell back on a permission error")
	}
}

func TestFallbackFailureIsReported(t *testing.T) {
	s, _ := newFakeSession(t, map[string]fakeResult{
		allReplicasQuery: {err: &mysql.MySQLError{Number: 1064}},
		replicaQuery:     {err: &mysql.MySQLError{Number: 1064}},
		allSlavesQuery:   {err: &mysql.MySQLError{Number: 1064}},
		slaveQuery:       {err: &mysql.MySQLError{Number: 1227}},
	})
	_, err := s.Server(context.Background())
	if got := classify(err); got != "permission denied (error 1227)" {
		t.Fatalf("classify = %q", got)
	}
}

func TestUnreadableResultsAreRefused(t *testing.T) {
	tests := map[string]map[string]fakeResult{
		"no thread columns": {allReplicasQuery: {cols: []string{"Something"}, rows: [][]any{{"x"}}}},
		"status row missing": {
			statusQuery:      {cols: []string{"Variable_name", "Value"}, rows: [][]any{{"Threads_connected", "3"}}},
			allReplicasQuery: {cols: newCols},
		},
		"status not a number": {
			statusQuery:      statusResult("many", "1"),
			allReplicasQuery: {cols: newCols},
		},
	}
	for name, results := range tests {
		t.Run(name, func(t *testing.T) {
			s, _ := newFakeSession(t, results)
			_, err := s.Server(context.Background())
			if !errors.Is(err, errUnexpectedResult) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The error and host columns are never read into the stats: planting
// secrets in them must not change what the channel holds.
func TestSensitiveReplicaColumnsAreNotRead(t *testing.T) {
	s, _ := newFakeSession(t, map[string]fakeResult{allReplicasQuery: {cols: newCols, rows: [][]any{
		replicaRow("Yes", "Yes", "1", "a"),
	}}})
	st, _ := s.Server(context.Background())
	obs := observe(st, 0, false)
	noSecretIn(t, obs, nil)
}

func TestChannelFromRowTreatsBadLagAsNull(t *testing.T) {
	cols := []string{"Replica_IO_Running", "Replica_SQL_Running", "Seconds_Behind_Source"}
	for _, lag := range []sql.RawBytes{nil, []byte("abc"), []byte("-4")} {
		ch, err := channelFromRow(cols, []sql.RawBytes{[]byte("Yes"), []byte("Yes"), lag})
		if err != nil || ch.Lag != nil {
			t.Errorf("lag %q: %+v, %v", lag, ch, err)
		}
	}
}
