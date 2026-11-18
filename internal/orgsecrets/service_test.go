package orgsecrets_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/orgsecrets"
)

var ctx = context.Background()

// leak is the distinctive value every test stores and then looks for in
// whatever the service hands back: it must never come out of a read path,
// an error or the stored row.
const leak = "s3cr3t-LEAK-7f2a"

var testKey = [32]byte{'o', 'r', 'g', 's', 'e', 'c', 'r', 'e', 't', 's'}

// memStore is the Store fake: rows by org and name.
type memStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]map[string]memRow
}

type memRow struct {
	sealed               []byte
	fieldNames           []string
	createdAt, updatedAt time.Time
}

func newMemStore() *memStore { return &memStore{rows: map[uuid.UUID]map[string]memRow{}} }

func (m *memStore) Upsert(_ context.Context, orgID uuid.UUID, name string, sealed []byte, fieldNames []string, at time.Time) (orgsecrets.Stored, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[orgID] == nil {
		m.rows[orgID] = map[string]memRow{}
	}
	r, ok := m.rows[orgID][name]
	if !ok {
		r.createdAt = at
	}
	r.sealed, r.fieldNames, r.updatedAt = slices.Clone(sealed), slices.Clone(fieldNames), at
	m.rows[orgID][name] = r
	return orgsecrets.Stored{Name: name, FieldNames: slices.Clone(fieldNames), CreatedAt: r.createdAt, UpdatedAt: r.updatedAt}, !ok, nil
}

func (m *memStore) Insert(_ context.Context, orgID uuid.UUID, name string, sealed []byte, fieldNames []string, at time.Time) (orgsecrets.Stored, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[orgID] == nil {
		m.rows[orgID] = map[string]memRow{}
	}
	if _, ok := m.rows[orgID][name]; ok {
		return orgsecrets.Stored{}, false, nil
	}
	m.rows[orgID][name] = memRow{sealed: slices.Clone(sealed), fieldNames: slices.Clone(fieldNames), createdAt: at, updatedAt: at}
	return orgsecrets.Stored{Name: name, FieldNames: slices.Clone(fieldNames), CreatedAt: at, UpdatedAt: at}, true, nil
}

func (m *memStore) Exists(_ context.Context, orgID uuid.UUID, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[orgID][name]
	return ok, nil
}

func (m *memStore) List(_ context.Context, orgID uuid.UUID) ([]orgsecrets.Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []orgsecrets.Stored
	for _, name := range slices.Sorted(maps.Keys(m.rows[orgID])) {
		r := m.rows[orgID][name]
		out = append(out, orgsecrets.Stored{Name: name, FieldNames: slices.Clone(r.fieldNames), CreatedAt: r.createdAt, UpdatedAt: r.updatedAt})
	}
	return out, nil
}

func (m *memStore) Sealed(_ context.Context, orgID uuid.UUID, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[orgID][name]
	if !ok {
		return nil, orgsecrets.ErrNotFound
	}
	return slices.Clone(r.sealed), nil
}

func (m *memStore) Delete(_ context.Context, orgID uuid.UUID, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[orgID][name]
	delete(m.rows[orgID], name)
	return ok, nil
}

// put writes a row as it is, for the AAD tests that move a ciphertext.
func (m *memStore) put(orgID uuid.UUID, name string, sealed []byte) {
	_, _, _ = m.Upsert(ctx, orgID, name, sealed, []string{"community"}, time.Now())
}

// fakeRefs is the CheckRefs port.
type fakeRefs struct {
	refs []orgsecrets.CheckRef
	err  error
}

func (f *fakeRefs) CheckRefs(context.Context, uuid.UUID) ([]orgsecrets.CheckRef, error) {
	return f.refs, f.err
}

// clock hands out a fixed time that tests move forward.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// plugins is the descriptor lookup of these tests: "srv" is a server
// plugin that takes a secret, "agt" an agent plugin that takes one (its
// secret lives in agent.yaml, not here) and "plain" a server plugin
// without one.
func plugins(name string) (checkplugins.Descriptor, bool) {
	switch name {
	case "srv":
		return checkplugins.Descriptor{Name: name, Runs: checkplugins.Server, Params: []checkplugins.ParamSpec{checkplugins.SecretParam()}}, true
	case "agt":
		return checkplugins.Descriptor{Name: name, Runs: checkplugins.Agent, Params: []checkplugins.ParamSpec{checkplugins.SecretParam()}}, true
	case "plain":
		return checkplugins.Descriptor{Name: name, Runs: checkplugins.Server, Params: []checkplugins.ParamSpec{{Name: "secret", Type: checkplugins.TypeString}}}, true
	}
	return checkplugins.Descriptor{}, false
}

type world struct {
	svc   *orgsecrets.Service
	store *memStore
	refs  *fakeRefs
	clock *clock
	org   uuid.UUID
}

func newWorld() *world {
	w := &world{store: newMemStore(), refs: &fakeRefs{}, clock: &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}, org: uuid.Must(uuid.NewV7())}
	w.svc = orgsecrets.NewService(orgsecrets.Deps{
		Store: w.store, Box: crypto.NewBox(testKey), Checks: w.refs, Now: w.clock.now, Plugins: plugins,
	})
	return w
}

// assertNoLeak fails when s carries the distinctive value.
func assertNoLeak(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, leak) {
		t.Fatalf("%s carries the secret value", where)
	}
}

func TestPutValidation(t *testing.T) {
	sixteen := map[string]string{}
	for i := range 16 {
		sixteen[fmt.Sprintf("f%02d", i)] = leak
	}
	seventeen := maps.Clone(sixteen)
	seventeen["f16"] = leak

	cases := []struct {
		name   string
		secret string
		fields map[string]string
		ok     bool
	}{
		{"valid", "snmp-core_1.v2", map[string]string{"community": leak}, true},
		{"name of 64 characters", strings.Repeat("n", 64), map[string]string{"community": leak}, true},
		{"16 fields", "many", sixteen, true},
		{"value of 4096 bytes", "big", map[string]string{"v": leak + strings.Repeat("x", 4096-len(leak))}, true},
		{"multi-byte UTF-8 value", "utf", map[string]string{"v": leak + "ünïcødé"}, true},
		{"empty name", "", map[string]string{"community": leak}, false},
		{"name of 65 characters", strings.Repeat("n", 65), map[string]string{"community": leak}, false},
		{"name with a slash", "a/b", map[string]string{"community": leak}, false},
		{"name with a space", "a b", map[string]string{"community": leak}, false},
		{"no fields", "none", map[string]string{}, false},
		{"nil fields", "none", nil, false},
		{"17 fields", "many", seventeen, false},
		{"empty field name", "f", map[string]string{"": leak}, false},
		{"field name with a colon", "f", map[string]string{"user:name": leak}, false},
		{"field name of 65 characters", "f", map[string]string{strings.Repeat("k", 65): leak}, false},
		{"empty value", "v", map[string]string{"community": ""}, false},
		{"value of 4097 bytes", "v", map[string]string{"community": leak + strings.Repeat("x", 4097-len(leak))}, false},
		{"invalid UTF-8", "v", map[string]string{"community": leak + "\xff\xfe"}, false},
		{"NUL in a value", "v", map[string]string{"community": leak + "\x00x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			_, err := w.svc.Put(ctx, w.org, tc.secret, tc.fields)
			if tc.ok {
				if err != nil {
					t.Fatalf("Put: %v", err)
				}
				return
			}
			if !errors.Is(err, orgsecrets.ErrInvalidInput) {
				t.Fatalf("Put = %v, want ErrInvalidInput", err)
			}
			assertNoLeak(t, "the validation error", err.Error())
			if list, _ := w.svc.List(ctx, w.org); len(list) != 0 {
				t.Fatalf("an invalid Put stored %+v", list)
			}
		})
	}
}

// TestPutValidationIsDeterministic: with several bad fields the refusal
// always names the first in name order, whatever the map's order.
func TestPutValidationIsDeterministic(t *testing.T) {
	w := newWorld()
	fields := map[string]string{"zeta": "", "mid": "", "alpha": "", "omega": ""}
	for range 50 {
		_, err := w.svc.Put(ctx, w.org, "sw", fields)
		if !errors.Is(err, orgsecrets.ErrInvalidInput) || !strings.Contains(err.Error(), "field alpha:") {
			t.Fatalf("Put = %v, want the refusal naming field alpha", err)
		}
	}
}

// TestPutSealsAndFieldsOpens: the stored row is the sealed JSON object
// (no clear value in it) with its sorted field names, and Fields opens it
// back to the same map.
func TestPutSealsAndFieldsOpens(t *testing.T) {
	w := newWorld()
	in := map[string]string{"user": "monitor", "auth_pass": leak, "priv_pass": leak + "-priv"}
	if _, err := w.svc.Put(ctx, w.org, "core-switch", in); err != nil {
		t.Fatal(err)
	}
	row := w.store.rows[w.org]["core-switch"]
	assertNoLeak(t, "the stored ciphertext", string(row.sealed))
	if want := []string{"auth_pass", "priv_pass", "user"}; !slices.Equal(row.fieldNames, want) {
		t.Fatalf("stored field names = %v, want %v", row.fieldNames, want)
	}
	got, err := w.svc.Fields(ctx, w.org, "core-switch")
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, in) {
		t.Fatal("Fields does not return the map Put stored")
	}
	if _, err := w.svc.Fields(ctx, w.org, "absent"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("Fields of an absent secret = %v, want ErrNotFound", err)
	}
}

// TestSealedRowIsBoundToOrgAndName: a ciphertext copied to another
// organization or another name does not open there, and the failure says
// nothing of the value.
func TestSealedRowIsBoundToOrgAndName(t *testing.T) {
	w := newWorld()
	if _, err := w.svc.Put(ctx, w.org, "a", map[string]string{"community": leak}); err != nil {
		t.Fatal(err)
	}
	sealed := w.store.rows[w.org]["a"].sealed
	other := uuid.Must(uuid.NewV7())
	w.store.put(w.org, "b", sealed)
	w.store.put(other, "a", sealed)

	for _, tc := range []struct {
		where string
		org   uuid.UUID
		name  string
	}{{"another name", w.org, "b"}, {"another organization", other, "a"}} {
		got, err := w.svc.Fields(ctx, tc.org, tc.name)
		if err == nil || errors.Is(err, orgsecrets.ErrNotFound) {
			t.Fatalf("%s: Fields = %v, %v; want an open failure", tc.where, got, err)
		}
		assertNoLeak(t, tc.where+": the error", err.Error())
	}
	if _, err := w.svc.Fields(ctx, w.org, "a"); err != nil {
		t.Fatalf("the original row no longer opens: %v", err)
	}
}

// TestPutReplacesWhole: a second Put replaces every field (a field left
// out is gone), keeps created_at and moves updated_at.
func TestPutReplacesWhole(t *testing.T) {
	w := newWorld()
	created := w.clock.t
	first, err := w.svc.Put(ctx, w.org, "sw", map[string]string{"community": "old", "extra": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("the first Put reports Created = false, want true (backlog 3.69)")
	}
	w.clock.t = created.Add(time.Hour)
	w.refs.refs = []orgsecrets.CheckRef{{Key: "snmp-core", Plugin: "srv", Params: map[string]any{"secret": "sw"}}}
	put, err := w.svc.Put(ctx, w.org, "sw", map[string]string{"community": leak})
	if err != nil {
		t.Fatal(err)
	}
	// Put returns the secret as stored, with the checks using it.
	if put.Name != "sw" || !put.CreatedAt.Equal(created) || !put.UpdatedAt.Equal(created.Add(time.Hour)) ||
		!slices.Equal(put.FieldNames, []string{"community"}) || !slices.Equal(put.UsedBy, []string{"snmp-core"}) {
		t.Fatalf("Put returned %+v", put)
	}
	if put.Created {
		t.Fatal("a Put over an existing secret reports Created = true, want false (backlog 3.69)")
	}
	assertNoLeak(t, "Put's result", fmt.Sprintf("%#v", put))
	list, err := w.svc.List(ctx, w.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].CreatedAt.Equal(created) || !list[0].UpdatedAt.Equal(created.Add(time.Hour)) ||
		!slices.Equal(list[0].FieldNames, []string{"community"}) {
		t.Fatalf("after the replace = %+v; want created %v, updated an hour later, fields [community]", list, created)
	}
	got, err := w.svc.Fields(ctx, w.org, "sw")
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, map[string]string{"community": leak}) {
		t.Fatal("the replace kept a field it did not send, or lost the new value")
	}
}

// TestListUsedByCountsServerChecksOnly: List names each secret with its
// field names, timestamps and the sorted, de-duplicated check keys of the
// server-run checks naming it — never a value. An agent-run check naming
// the same secret reads agent.yaml and is not counted, nor is a plugin
// without the secret parameter's shape, an unknown plugin or a check
// naming another secret.
func TestListUsedByCountsServerChecksOnly(t *testing.T) {
	w := newWorld()
	for _, name := range []string{"sw", "unused"} {
		if _, err := w.svc.Put(ctx, w.org, name, map[string]string{"community": leak}); err != nil {
			t.Fatal(err)
		}
	}
	w.refs.refs = []orgsecrets.CheckRef{
		{Key: "snmp-b", Plugin: "srv", Params: map[string]any{"secret": "sw"}},
		{Key: "snmp-a", Plugin: "srv", Params: map[string]any{"secret": "sw"}},
		{Key: "snmp-a", Plugin: "srv", Params: map[string]any{"secret": "sw"}}, // the same key at another binding
		{Key: "mysql", Plugin: "agt", Params: map[string]any{"secret": "sw"}},
		{Key: "plain", Plugin: "plain", Params: map[string]any{"secret": "sw"}},
		{Key: "gone", Plugin: "removed-plugin", Params: map[string]any{"secret": "sw"}},
		{Key: "other", Plugin: "srv", Params: map[string]any{"secret": "elsewhere"}},
		{Key: "unset", Plugin: "srv", Params: map[string]any{}},
		{Key: "wrong-type", Plugin: "srv", Params: map[string]any{"secret": 7}},
	}
	list, err := w.svc.List(ctx, w.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "sw" || list[1].Name != "unused" {
		t.Fatalf("List = %+v, want sw and unused", list)
	}
	if want := []string{"snmp-a", "snmp-b"}; !slices.Equal(list[0].UsedBy, want) {
		t.Fatalf("sw used by %v, want %v", list[0].UsedBy, want)
	}
	if len(list[1].UsedBy) != 0 {
		t.Fatalf("unused used by %v, want none", list[1].UsedBy)
	}
	assertNoLeak(t, "the list", fmt.Sprintf("%#v", list))

	names, err := w.svc.Names(ctx, w.org)
	if err != nil || !slices.Equal(names, []string{"sw", "unused"}) {
		t.Fatalf("Names = %v, %v", names, err)
	}

	w.refs.err = errors.New("db down")
	if _, err := w.svc.List(ctx, w.org); err == nil {
		t.Fatal("List swallowed the check references' failure")
	}
}

// TestDelete: an absent secret is ErrNotFound; one a server check names is
// ErrConflict naming the check keys (and nothing is deleted); otherwise it
// is gone.
func TestDelete(t *testing.T) {
	w := newWorld()
	if err := w.svc.Delete(ctx, w.org, "absent"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("Delete of an absent secret = %v, want ErrNotFound", err)
	}
	if _, err := w.svc.Put(ctx, w.org, "sw", map[string]string{"community": leak}); err != nil {
		t.Fatal(err)
	}
	w.refs.refs = []orgsecrets.CheckRef{
		{Key: "snmp-host", Plugin: "srv", Params: map[string]any{"secret": "sw"}},
		{Key: "snmp-core", Plugin: "srv", Params: map[string]any{"secret": "sw"}},
		{Key: "mysql", Plugin: "agt", Params: map[string]any{"secret": "sw"}},
	}
	err := w.svc.Delete(ctx, w.org, "sw")
	if !errors.Is(err, orgsecrets.ErrConflict) || !strings.Contains(err.Error(), "snmp-core, snmp-host") || strings.Contains(err.Error(), "mysql") {
		t.Fatalf("Delete of a used secret = %v, want ErrConflict naming snmp-core, snmp-host only", err)
	}
	// The keys also come as a list, for the API's structured 409.
	var inUse *orgsecrets.InUseError
	if !errors.As(err, &inUse) || !slices.Equal(inUse.Keys, []string{"snmp-core", "snmp-host"}) {
		t.Fatalf("Delete of a used secret = %#v, want an InUseError with keys snmp-core, snmp-host", err)
	}
	if _, err := w.svc.Fields(ctx, w.org, "sw"); err != nil {
		t.Fatalf("the refused delete removed the secret: %v", err)
	}

	// An agent-run check naming it does not hold it.
	w.refs.refs = w.refs.refs[2:]
	if err := w.svc.Delete(ctx, w.org, "sw"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := w.svc.Fields(ctx, w.org, "sw"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("Fields after the delete = %v, want ErrNotFound", err)
	}
	if err := w.svc.Delete(ctx, w.org, "a/b"); !errors.Is(err, orgsecrets.ErrNotFound) {
		t.Fatalf("Delete of an invalid name = %v, want ErrNotFound", err)
	}
}

// TestCreateOnly: Create stores a new secret like Put, but refuses a name
// the organization already has with ErrExists and leaves that secret as
// it was; it validates like Put.
func TestCreateOnly(t *testing.T) {
	w := newWorld()
	created, err := w.svc.Create(ctx, w.org, "sw", map[string]string{"community": leak})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "sw" || !slices.Equal(created.FieldNames, []string{"community"}) || !created.CreatedAt.Equal(w.clock.t) {
		t.Fatalf("Create returned %+v", created)
	}
	if !created.Created {
		t.Fatal("Create reports Created = false, want true")
	}
	assertNoLeak(t, "Create's result", fmt.Sprintf("%#v", created))
	w.clock.t = w.clock.t.Add(time.Hour)
	if _, err := w.svc.Create(ctx, w.org, "sw", map[string]string{"community": "other"}); !errors.Is(err, orgsecrets.ErrExists) {
		t.Fatalf("Create of an existing name = %v, want ErrExists", err)
	}
	got, err := w.svc.Fields(ctx, w.org, "sw")
	if err != nil || !maps.Equal(got, map[string]string{"community": leak}) {
		t.Fatalf("the refused create changed the secret (%v)", err)
	}
	if _, err := w.svc.Create(ctx, w.org, "a/b", map[string]string{"community": "x"}); !errors.Is(err, orgsecrets.ErrInvalidInput) {
		t.Fatalf("Create of an invalid name = %v, want ErrInvalidInput", err)
	}
}

// TestWriteSurvivesAFailedUsedByLookup: the "used by" lookup runs after
// the write has committed. When it fails, Put and Create still answer with
// the stored secret (UsedBy unknown, left empty) instead of an error, so
// the API answers success and audits the write it made; a 500 would leave
// a stored credential without an audit entry.
func TestWriteSurvivesAFailedUsedByLookup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(w *world, fields map[string]string) (orgsecrets.Secret, error)
	}{
		{"put", func(w *world, f map[string]string) (orgsecrets.Secret, error) {
			return w.svc.Put(ctx, w.org, "sw", f)
		}},
		{"create", func(w *world, f map[string]string) (orgsecrets.Secret, error) {
			return w.svc.Create(ctx, w.org, "sw", f)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			w.refs.refs = []orgsecrets.CheckRef{{Key: "snmp-core", Plugin: "srv", Params: map[string]any{"secret": "sw"}}}
			w.refs.err = errors.New("db down")
			got, err := tc.write(w, map[string]string{"community": leak})
			if err != nil {
				t.Fatalf("write after a failed used-by lookup = %v, want the stored secret", err)
			}
			if got.Name != "sw" || !slices.Equal(got.FieldNames, []string{"community"}) || !got.UpdatedAt.Equal(w.clock.t) || len(got.UsedBy) != 0 {
				t.Fatalf("write returned %+v, want sw as stored with UsedBy unknown", got)
			}
			assertNoLeak(t, "the result", fmt.Sprintf("%#v", got))
			fields, err := w.svc.Fields(ctx, w.org, "sw")
			if err != nil || !maps.Equal(fields, map[string]string{"community": leak}) {
				t.Fatalf("the secret was not stored (%v)", err)
			}
		})
	}
}
