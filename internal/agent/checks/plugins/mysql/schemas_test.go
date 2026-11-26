package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

var sizesOn = map[string]any{"schema_sizes": true}

func collectWith(t *testing.T, p *plugin, params map[string]any) []checks.Observation {
	t.Helper()
	obs, err := p.CollectWithSecret(context.Background(), params, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	noSecretIn(t, obs, err)
	return obs
}

func TestSchemaSizesOffSendsNoQuery(t *testing.T) {
	for _, params := range []map[string]any{nil, {"schema_sizes": false}} {
		sess := &fakeSession{stats: serverStats{MaxConnections: 10}, schemas: []schemaSize{{Name: "app", Bytes: 1}}}
		obs := collectWith(t, newTestPlugin(sess, nil, nil), params)
		if sess.schemasQueried {
			t.Errorf("%v: sizes queried", params)
		}
		for _, o := range obs {
			if strings.HasPrefix(o.Metric, "mysql.schema") {
				t.Errorf("%v: %+v", params, o)
			}
		}
	}
}

func TestSchemaSizes(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, schemas: []schemaSize{{Name: "app", Bytes: 16384}, {Name: "shop", Bytes: 0}}}
	obs := collectWith(t, newTestPlugin(sess, nil, nil), sizesOn)
	mustValue(t, obs, "app", "mysql.schema_size_bytes", 16384)
	mustValue(t, obs, "shop", "mysql.schema_size_bytes", 0)
	mustValue(t, obs, "", "mysql.schemas_unreported", 0)
	mustValue(t, obs, "", "mysql.up", 1)
}

// The sizes query is the slow one: when it fails, the server is still up
// and every other metric is reported.
func TestSchemaSizesFailureKeepsTheRoundUp(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&mysql.MySQLError{Number: 1142, Message: "denied to " + plantedUser}, "mysql: permission denied (error 1142)"},
		{context.DeadlineExceeded, "mysql: timed out"},
		{errors.New("odd " + plantedHost), "mysql: " + classify(errors.New("odd"))},
	}
	for _, tt := range tests {
		sess := &fakeSession{stats: serverStats{MaxConnections: 10, Connections: 2}, schemasErr: tt.err}
		obs := collectWith(t, newTestPlugin(sess, nil, nil), sizesOn)
		mustValue(t, obs, "", "mysql.up", 1)
		mustValue(t, obs, "", "mysql.connections", 2)
		mustErr(t, obs, "", "mysql.schemas_unreported", tt.want)
		for _, o := range obs {
			if o.Metric == "mysql.schema_size_bytes" {
				t.Errorf("size reported after a failed query: %+v", o)
			}
		}
	}
}

func TestSchemaCap(t *testing.T) {
	var ss []schemaSize
	for i := 0; i < maxSchemas+44; i++ {
		ss = append(ss, schemaSize{Name: fmt.Sprintf("s%03d", i), Bytes: 1})
	}
	total := 1000
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, schemas: ss, schemasTotal: &total}
	obs := collectWith(t, newTestPlugin(sess, nil, nil), sizesOn)
	n := 0
	for _, o := range obs {
		if o.Metric == "mysql.schema_size_bytes" {
			n++
		}
	}
	if n != maxSchemas {
		t.Errorf("%d schemas reported, want %d", n, maxSchemas)
	}
	mustValue(t, obs, "", "mysql.schemas_unreported", float64(1000-maxSchemas))
}

func TestUnreportableSchemaNameIsCounted(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, schemas: []schemaSize{{Name: "a\nb", Bytes: 1}, {Name: "ok", Bytes: 2}}}
	obs, err := newTestPlugin(sess, nil, nil).CollectWithSecret(context.Background(), sizesOn, goodFields)
	if err == nil || !strings.Contains(err.Error(), "cannot be reported") {
		t.Errorf("err = %v", err)
	}
	for _, o := range obs {
		if !checks.ReportableItem(o.Item) {
			t.Errorf("unreportable item %q", o.Item)
		}
	}
	mustValue(t, obs, "ok", "mysql.schema_size_bytes", 2)
	mustValue(t, obs, "", "mysql.schemas_unreported", 1)
}

func TestDownRoundFailsTheUnreportedCountOnlyWhenAsked(t *testing.T) {
	sess := &fakeSession{statsErr: errors.New("boom")}
	on := collectWith(t, newTestPlugin(sess, nil, nil), sizesOn)
	mustValue(t, on, "", "mysql.up", 0)
	if o, ok := find(on, "", "mysql.schemas_unreported"); !ok || o.Err == "" {
		t.Errorf("schemas_unreported on a failed round = %+v", o)
	}
	off := collectWith(t, newTestPlugin(sess, nil, nil), nil)
	if _, ok := find(off, "", "mysql.schemas_unreported"); ok {
		t.Error("schemas_unreported without schema_sizes")
	}
}

func TestSchemasQuery(t *testing.T) {
	s, f := newFakeSession(t, map[string]fakeResult{schemasQuery: {
		cols: []string{"TABLE_SCHEMA", "size", "total"},
		rows: [][]any{{"app", "16384", 3}, {"shop", "0", 3}},
	}})
	got, total, err := s.Schemas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 2 || got[0] != (schemaSize{Name: "app", Bytes: 16384}) || got[1] != (schemaSize{Name: "shop"}) {
		t.Fatalf("schemas %+v total %d", got, total)
	}
	if len(f.seen) != 1 || f.seen[0] != schemasQuery {
		t.Fatalf("statements %v", f.seen)
	}
	if !strings.HasSuffix(schemasQuery, "LIMIT "+strconv.Itoa(maxSchemas)) {
		t.Errorf("the query is not capped at maxSchemas: %s", schemasQuery)
	}
	for _, sys := range []string{"'mysql'", "'information_schema'", "'performance_schema'", "'sys'"} {
		if !strings.Contains(schemasQuery, sys) {
			t.Errorf("system schema %s not excluded", sys)
		}
	}
}

func TestSchemasQueryErrorIsReturned(t *testing.T) {
	s, _ := newFakeSession(t, map[string]fakeResult{schemasQuery: {err: &mysql.MySQLError{Number: 1142}}})
	if _, _, err := s.Schemas(context.Background()); err == nil {
		t.Fatal("no error")
	}
}
