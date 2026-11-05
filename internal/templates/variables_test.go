// A template's variables follow the fields' active switch (spec 7.75 owner
// decision 4, plan decision 10): a save carries the stored value of every
// inactive or removed field over verbatim — a sealed secret stays the same
// sealed bytes — and neither validates nor requires it. Writes name the
// revision they are based on.
package templates_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

func inactive() *bool { b := false; return &b }

func rev(n int64) *int64 { return &n }

// storedSealed returns the stored ciphertext of name, failing when it is
// not sealed.
func storedSealed(t *testing.T, f fixture, id uuid.UUID, name string) []byte {
	t.Helper()
	ct, ok := schema.SealedBytes(f.store.byID[id].ExtraVars[name])
	if !ok {
		t.Fatalf("stored %s = %#v, want a sealed value", name, f.store.byID[id].ExtraVars[name])
	}
	return ct
}

// saveVia names the two writes a person's variables reach the store
// through: the template PUT and the variables PUT.
var saveVia = []struct {
	name string
	save func(f fixture, cur templates.Template, sc *schema.Schema, vars map[string]any) (templates.Template, error)
}{
	{"template", func(f fixture, cur templates.Template, sc *schema.Schema, vars map[string]any) (templates.Template, error) {
		return f.svc.Update(ctx, f.org, cur.ID, templates.UpdateInput{
			Name: cur.Name, PlaybookID: cur.PlaybookID, ExtraVarsSchema: sc, ExtraVars: vars,
		})
	}},
	{"variables", func(f fixture, cur templates.Template, sc *schema.Schema, vars map[string]any) (templates.Template, error) {
		return f.svc.UpdateVariables(ctx, f.org, cur.ID, templates.VariablesInput{Schema: sc, ExtraVars: vars})
	}},
}

// TestInactiveFieldKeepsItsStoredValue is Review Focus 2: a secret field
// switched inactive and saved keeps its sealed bytes; switched active again
// with the keep marker a read returned, the same bytes are back in use.
func TestInactiveFieldKeepsItsStoredValue(t *testing.T) {
	for _, via := range saveVia {
		t.Run(via.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
				{Name: "token", Type: schema.TypeSecret, Required: true},
				{Name: "region", Type: schema.TypeString},
			}}
			in.ExtraVars = map[string]any{"token": "s3cret-token", "region": "eu"}
			created, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			sealed := storedSealed(t, f, created.ID, "token")

			off := &schema.Schema{Fields: []schema.Field{
				{Name: "token", Type: schema.TypeSecret, Required: true, Active: inactive()},
				{Name: "region", Type: schema.TypeString},
			}}
			if _, err := via.save(f, created, off, map[string]any{"region": "eu-west"}); err != nil {
				t.Fatalf("save with token inactive: %v", err)
			}
			if got := storedSealed(t, f, created.ID, "token"); !bytes.Equal(got, sealed) {
				t.Fatal("an inactive secret's sealed bytes must stay identical across a save")
			}
			if f.store.byID[created.ID].ExtraVars["region"] != "eu-west" {
				t.Fatalf("region = %#v, want the saved value", f.store.byID[created.ID].ExtraVars["region"])
			}
			// A second save while inactive changes nothing either.
			if _, err := via.save(f, created, off, map[string]any{"region": "eu-west"}); err != nil {
				t.Fatalf("second save with token inactive: %v", err)
			}
			if got := storedSealed(t, f, created.ID, "token"); !bytes.Equal(got, sealed) {
				t.Fatal("a second save must keep the sealed bytes as well")
			}

			read, err := f.svc.Get(ctx, f.org, created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !reflect.DeepEqual(read.ExtraVars["token"], schema.Mask()) {
				t.Fatalf("read token = %#v, want the mask", read.ExtraVars["token"])
			}
			on := &schema.Schema{Fields: []schema.Field{
				{Name: "token", Type: schema.TypeSecret, Required: true},
				{Name: "region", Type: schema.TypeString},
			}}
			if _, err := via.save(f, created, on, map[string]any{"token": read.ExtraVars["token"], "region": "eu-west"}); err != nil {
				t.Fatalf("save with token active again: %v", err)
			}
			if got := storedSealed(t, f, created.ID, "token"); !bytes.Equal(got, sealed) {
				t.Fatal("reactivated with the keep marker, the sealed bytes must be the original ones")
			}
			read, err = f.svc.Get(ctx, f.org, created.ID)
			if err != nil || !reflect.DeepEqual(read.ExtraVars["token"], schema.Mask()) {
				t.Fatalf("read token after reactivation = %#v, %v; want the mask", read.ExtraVars["token"], err)
			}
		})
	}
}

// TestRemovedFieldKeepsItsStoredValue: a field removed from the schema
// keeps its stored value, a read still shows it, and adding the field back
// with the value the read returned brings it back.
func TestRemovedFieldKeepsItsStoredValue(t *testing.T) {
	for _, via := range saveVia {
		t.Run(via.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			withRegion := &schema.Schema{Fields: []schema.Field{
				{Name: "region", Type: schema.TypeString},
				{Name: "token", Type: schema.TypeSecret},
			}}
			in.ExtraVarsSchema = withRegion
			in.ExtraVars = map[string]any{"region": "eu", "token": "s3cret-token"}
			created, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			sealed := storedSealed(t, f, created.ID, "token")

			if _, err := via.save(f, created, &schema.Schema{}, map[string]any{}); err != nil {
				t.Fatalf("save without fields: %v", err)
			}
			stored := f.store.byID[created.ID].ExtraVars
			if stored["region"] != "eu" {
				t.Fatalf("stored region = %#v, want it kept", stored["region"])
			}
			if got := storedSealed(t, f, created.ID, "token"); !bytes.Equal(got, sealed) {
				t.Fatal("a removed secret field keeps its sealed bytes")
			}

			read, err := f.svc.Get(ctx, f.org, created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if read.ExtraVars["region"] != "eu" || !reflect.DeepEqual(read.ExtraVars["token"], schema.Mask()) {
				t.Fatalf("read after removal = %#v, want region and a masked token", read.ExtraVars)
			}
			if _, err := via.save(f, created, withRegion, read.ExtraVars); err != nil {
				t.Fatalf("save with the fields added back: %v", err)
			}
			if f.store.byID[created.ID].ExtraVars["region"] != "eu" {
				t.Fatalf("region after adding it back = %#v", f.store.byID[created.ID].ExtraVars["region"])
			}
			if got := storedSealed(t, f, created.ID, "token"); !bytes.Equal(got, sealed) {
				t.Fatal("a secret added back with the mask keeps its sealed bytes")
			}
		})
	}
}

// TestSaveIgnoresInvalidValuesUnderInactiveFields: an inactive field is
// neither type-checked nor required, so a value that no longer fits it
// does not stop a save, and stays stored as it is.
func TestSaveIgnoresInvalidValuesUnderInactiveFields(t *testing.T) {
	for _, via := range saveVia {
		t.Run(via.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "count", Type: schema.TypeString}}}
			in.ExtraVars = map[string]any{"count": "x"}
			created, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			sc := &schema.Schema{Fields: []schema.Field{{Name: "count", Type: schema.TypeInt, Required: true, Active: inactive()}}}
			if _, err := via.save(f, created, sc, map[string]any{}); err != nil {
				t.Fatalf("save with an invalid value under an inactive required field: %v", err)
			}
			if f.store.byID[created.ID].ExtraVars["count"] != "x" {
				t.Fatalf("count = %#v, want the stored value kept", f.store.byID[created.ID].ExtraVars["count"])
			}
			// The same field active: its value is checked again.
			sc.Fields[0].Active = nil
			if _, err := via.save(f, created, sc, map[string]any{"count": "x"}); problemPaths(err) == nil {
				t.Fatalf("save with an invalid value under an active field = %v, want a validation error", err)
			}
		})
	}
}

// TestSaveDropsAnOmittedActiveValue: only inactive and removed fields are
// carried over; an active field the body omits is cleared, as before.
func TestSaveDropsAnOmittedActiveValue(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString}}}
	in.ExtraVars = map[string]any{"region": "eu"}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{Schema: in.ExtraVarsSchema, ExtraVars: map[string]any{}}); err != nil {
		t.Fatalf("UpdateVariables: %v", err)
	}
	if _, ok := f.store.byID[created.ID].ExtraVars["region"]; ok {
		t.Fatalf("an omitted active value must be cleared: %#v", f.store.byID[created.ID].ExtraVars)
	}
}

// TestSaveWithoutSchemaReplacesEveryValue: without a schema there is no
// active switch, and a save replaces the values wholesale, as before.
func TestSaveWithoutSchemaReplacesEveryValue(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString, Active: inactive()}}}
	in.ExtraVars = map[string]any{"region": "eu"}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{ExtraVars: map[string]any{"other": "x"}}); err != nil {
		t.Fatalf("UpdateVariables: %v", err)
	}
	if got := f.store.byID[created.ID].ExtraVars; !reflect.DeepEqual(got, map[string]any{"other": "x"}) {
		t.Fatalf("extra vars = %#v, want exactly the body", got)
	}
}

// TestTemplateWritesHonourTheRevision: both writes check IfRevision when it
// is given (412 at the API), skip the check when it is nil, and bump the
// revision. UpdateVariables changes nothing but the variables and keeps the
// variables source columns.
func TestTemplateWritesHonourTheRevision(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Revision != 1 || created.VarsMode != templates.ModeOff {
		t.Fatalf("created revision %d mode %q, want 1, off", created.Revision, created.VarsMode)
	}
	up := templates.UpdateInput{Name: "renamed", PlaybookID: f.playbook, IfRevision: rev(1)}
	got, err := f.svc.Update(ctx, f.org, created.ID, up)
	if err != nil || got.Revision != 2 {
		t.Fatalf("Update(if 1) = rev %d, %v; want 2", got.Revision, err)
	}
	if _, err := f.svc.Update(ctx, f.org, created.ID, up); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("Update(stale) = %v, want ErrRevisionMismatch", err)
	}
	up.IfRevision = nil
	if got, err = f.svc.Update(ctx, f.org, created.ID, up); err != nil || got.Revision != 3 {
		t.Fatalf("Update(nil) = rev %d, %v; want 3", got.Revision, err)
	}

	sc := &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString}}}
	vin := templates.VariablesInput{Schema: sc, ExtraVars: map[string]any{"region": "eu"}, IfRevision: rev(3)}
	got, err = f.svc.UpdateVariables(ctx, f.org, created.ID, vin)
	if err != nil || got.Revision != 4 || got.Name != "renamed" || got.ExtraVars["region"] != "eu" || got.VarsMode != templates.ModeOff {
		t.Fatalf("UpdateVariables(if 3) = %+v, %v", got, err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, vin); !errors.Is(err, templates.ErrRevisionMismatch) {
		t.Fatalf("UpdateVariables(stale) = %v, want ErrRevisionMismatch", err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, uuid.Must(uuid.NewV7()), templates.VariablesInput{}); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("UpdateVariables(unknown) = %v, want ErrNotFound", err)
	}
}

// TestUpdateVariablesChecksLikeASave: the variables write runs the same
// value and schema checks as a template save.
func TestUpdateVariablesChecksLikeASave(t *testing.T) {
	f := newFixture()
	created, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bad := &schema.Schema{Fields: []schema.Field{{Name: "Bad Name", Type: schema.TypeString}}}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{Schema: bad}); !errors.Is(err, templates.ErrBadSchema) {
		t.Fatalf("UpdateVariables(bad schema) = %v, want ErrBadSchema", err)
	}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{ExtraVars: map[string]any{"ansible_ssh_common_args": "-o x"}}); problemPaths(err) == nil {
		t.Fatalf("UpdateVariables(reserved name) = %v, want a validation error", err)
	}
	sc := &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString}}}
	if _, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{Schema: sc, ExtraVars: map[string]any{"unknown": "x"}}); problemPaths(err) == nil {
		t.Fatalf("UpdateVariables(undeclared value) = %v, want a validation error", err)
	}
}

// TestUpdateVariablesRefusesAProjection: a projection's variables belong to
// its installation-owned definition.
func TestUpdateVariablesRefusesAProjection(t *testing.T) {
	f := newFixture()
	sharedID := uuid.Must(uuid.NewV7())
	existing := templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "shared-tpl", PlaybookID: f.playbook,
		ExtraVars: map[string]any{"k": "v"}, SharedTemplateID: &sharedID,
	}
	f.store.byID[existing.ID] = existing
	_, err := f.svc.UpdateVariables(ctx, f.org, existing.ID, templates.VariablesInput{ExtraVars: map[string]any{"k": "changed"}})
	if !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("UpdateVariables(projection) = %v, want ErrInvalidInput", err)
	}
	if f.store.byID[existing.ID].ExtraVars["k"] != "v" {
		t.Fatal("a refused write must change nothing")
	}
}

// TestProjectionSaveCarriesInactiveValuesOver: the template dialog sends a
// projection's active values only; with the inactive values carried over
// the body equals the definition's, so a scope edit is not refused as an
// extra vars change.
func TestProjectionSaveCarriesInactiveValuesOver(t *testing.T) {
	f := newFixture()
	sharedID := uuid.Must(uuid.NewV7())
	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "region", Type: schema.TypeString},
		{Name: "size", Type: schema.TypeString, Active: inactive()},
	}}
	existing := templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: "shared-tpl", PlaybookID: f.playbook,
		ExtraVars: map[string]any{"region": "eu", "size": "xl"}, ExtraVarsSchema: sc, SharedTemplateID: &sharedID,
	}
	f.store.byID[existing.ID] = existing
	if _, err := f.svc.Update(ctx, f.org, existing.ID, templates.UpdateInput{
		Name: existing.Name, PlaybookID: existing.PlaybookID, ExtraVarsSchema: sc,
		ExtraVars: map[string]any{"region": "eu"}, Scope: templates.Scope{HostIDs: []uuid.UUID{f.managed}},
	}); err != nil {
		t.Fatalf("Update(projection, active values only) = %v, want nil", err)
	}
	if f.store.byID[existing.ID].ExtraVars["size"] != "xl" {
		t.Fatalf("size = %#v, want it kept", f.store.byID[existing.ID].ExtraVars["size"])
	}
}

// TestCarriedLegacyPlaintextSecretIsSealed: a secret stored as plaintext
// before sealing existed (backlog 2.47) is sealed by the save that carries
// it over — under a field switched inactive, under one removed from the
// schema, or under one retyped away from secret and switched inactive, at
// the top level or nested — so it never reads in the clear, neither in the
// write's own response nor on a later read. A sealed value is still
// carried verbatim.
func TestCarriedLegacyPlaintextSecretIsSealed(t *testing.T) {
	flat := &schema.Schema{Fields: []schema.Field{
		{Name: "token", Type: schema.TypeSecret},
		{Name: "region", Type: schema.TypeString},
	}}
	nested := &schema.Schema{Fields: []schema.Field{
		{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{{Name: "password", Type: schema.TypeSecret}}},
		{Name: "region", Type: schema.TypeString},
	}}
	const plain = "legacy-plain-5d1"
	cases := []struct {
		name   string
		base   *schema.Schema
		legacy map[string]any
		next   *schema.Schema
	}{
		{"inactive", flat, map[string]any{"token": plain, "region": "eu"}, &schema.Schema{Fields: []schema.Field{
			{Name: "token", Type: schema.TypeSecret, Active: inactive()},
			{Name: "region", Type: schema.TypeString},
		}}},
		{"removed", flat, map[string]any{"token": plain, "region": "eu"}, &schema.Schema{Fields: []schema.Field{
			{Name: "region", Type: schema.TypeString},
		}}},
		{"retyped and inactive", flat, map[string]any{"token": plain, "region": "eu"}, &schema.Schema{Fields: []schema.Field{
			{Name: "token", Type: schema.TypeString, Active: inactive()},
			{Name: "region", Type: schema.TypeString},
		}}},
		{"nested, retyped and inactive", nested, map[string]any{"db": map[string]any{"password": plain}, "region": "eu"}, &schema.Schema{Fields: []schema.Field{
			{Name: "db", Type: schema.TypeObject, Active: inactive(), Fields: []schema.Field{{Name: "password", Type: schema.TypeString}}},
			{Name: "region", Type: schema.TypeString},
		}}},
		{"nested and removed", nested, map[string]any{"db": map[string]any{"password": plain}, "region": "eu"}, &schema.Schema{Fields: []schema.Field{
			{Name: "region", Type: schema.TypeString},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			in.ExtraVarsSchema = c.base
			in.ExtraVars = map[string]any{"region": "eu"}
			created, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			revertToLegacy(t, f, created, c.legacy)

			put, err := f.svc.UpdateVariables(ctx, f.org, created.ID, templates.VariablesInput{Schema: c.next, ExtraVars: map[string]any{"region": "eu"}})
			if err != nil {
				t.Fatalf("UpdateVariables: %v", err)
			}
			read, err := f.svc.Get(ctx, f.org, created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			for what, vars := range map[string]map[string]any{
				"stored": f.store.byID[created.ID].ExtraVars, "PUT response": put.ExtraVars, "GET": read.ExtraVars,
			} {
				b, err := json.Marshal(vars)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), plain) {
					t.Fatalf("%s holds the legacy plaintext: %s", what, b)
				}
			}
			if _, top := c.legacy["token"]; top {
				if got := openStored(t, testBox(), f, created, "token"); got != plain {
					t.Fatalf("carried legacy secret opens to %q", got)
				}
			}
		})
	}
}

// TestReadModifyWriteIsGuardedWithoutIfMatch: a write without If-Match is
// still based on the row it read, so another write landing between that
// read and this write answers ErrRevisionMismatch (412) instead of being
// undone.
func TestReadModifyWriteIsGuardedWithoutIfMatch(t *testing.T) {
	writes := map[string]func(f fixture, id uuid.UUID) error{
		"template": func(f fixture, id uuid.UUID) error {
			_, err := f.svc.Update(ctx, f.org, id, templates.UpdateInput{Name: "renamed", PlaybookID: f.playbook})
			return err
		},
		"variables": func(f fixture, id uuid.UUID) error {
			_, err := f.svc.UpdateVariables(ctx, f.org, id, templates.VariablesInput{ExtraVars: map[string]any{"a": "b"}})
			return err
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			created, err := f.svc.Create(ctx, f.org, f.in())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			f.store.beforeWrite = func() {
				other := f.store.byID[created.ID]
				other.Revision++
				f.store.byID[created.ID] = other
			}
			if err := write(f, created.ID); !errors.Is(err, templates.ErrRevisionMismatch) {
				t.Fatalf("write racing another = %v, want ErrRevisionMismatch", err)
			}
			if err := write(f, created.ID); err != nil {
				t.Fatalf("write without a race = %v, want nil", err)
			}
		})
	}
}

// TestVariablesReviewNamesValuesNotSent (backlog 7.87): a stored secret
// under a field that is no longer a secret is left out of a run until a
// person enters its value again; the variables page names it.
func TestVariablesReviewNamesValuesNotSent(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
		{Name: "region", Type: schema.TypeString},
		{Name: "token", Type: schema.TypeSecret},
	}}
	in.ExtraVars = map[string]any{"region": "eu", "token": "s3cret-token"}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	view, err := f.svc.Variables(ctx, f.org, created.ID)
	if err != nil {
		t.Fatalf("Variables: %v", err)
	}
	if len(view.Review.NotSent) != 0 {
		t.Fatalf("NotSent = %v with token a secret field, want none", view.Review.NotSent)
	}

	// The field retyped to a plain string over its sealed value, as an
	// automatic apply before the hold for that change left it.
	stored := f.store.byID[created.ID]
	stored.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
		{Name: "region", Type: schema.TypeString},
		{Name: "token", Type: schema.TypeString},
	}}
	f.store.byID[created.ID] = stored
	view, err = f.svc.Variables(ctx, f.org, created.ID)
	if err != nil {
		t.Fatalf("Variables: %v", err)
	}
	if !reflect.DeepEqual(view.Review.NotSent, []string{"token"}) {
		t.Fatalf("NotSent = %v, want [token]", view.Review.NotSent)
	}
}
