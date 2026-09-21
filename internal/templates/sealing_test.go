// Secret-typed extra vars are sealed at rest (final review triage, Task 7's
// hardening item): a template's secret values never sit in the database as
// plaintext. Create and Update seal them with the template's own AAD; an
// Update that sends back the masked marker (the value a read returned) or
// an explicit keep marker keeps the stored secret — a sealed envelope, or a
// legacy plaintext string from a template saved before sealing existed,
// which that save seals (backlog 2.47, 2.52); the claim path opens them
// again (internal/app's template lookup).
package templates_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

func secretSchema() *schema.Schema {
	return &schema.Schema{Fields: []schema.Field{
		{Name: "db_pass", Type: schema.TypeSecret},
		{Name: "region", Type: schema.TypeString},
	}}
}

// openStored opens the stored value of name, failing when it is not sealed.
func openStored(t *testing.T, box *crypto.Box, f fixture, tmpl templates.Template, name string) string {
	t.Helper()
	stored := f.store.byID[tmpl.ID]
	ct, ok := schema.SealedBytes(stored.ExtraVars[name])
	if !ok {
		t.Fatalf("stored %s = %#v, want a sealed value", name, stored.ExtraVars[name])
	}
	plain, err := box.OpenAAD(ct, templates.ExtraVarsAAD(tmpl.OrgID, tmpl.ID))
	if err != nil {
		t.Fatalf("open stored %s: %v", name, err)
	}
	return string(plain)
}

func TestSecretExtraVarsAreSealedAtRest(t *testing.T) {
	f := newFixture()
	box := testBox()
	in := f.in()
	in.ExtraVarsSchema = secretSchema()
	in.ExtraVars = map[string]any{"db_pass": "hunter2-first", "region": "eu-central"}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-first" {
		t.Fatalf("sealed value opens to %q", got)
	}
	if f.store.byID[created.ID].ExtraVars["region"] != "eu-central" {
		t.Fatalf("a plain value must be stored as is: %#v", f.store.byID[created.ID].ExtraVars)
	}
	firstCiphertext := f.store.byID[created.ID].ExtraVars["db_pass"]

	update := func(vars map[string]any) (templates.Template, error) {
		return f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
			Name: in.Name, PlaybookID: in.PlaybookID, ExtraVarsSchema: secretSchema(), ExtraVars: vars,
		})
	}
	// The masked marker a read returned keeps the stored secret.
	if _, err := update(map[string]any{"db_pass": schema.Mask(), "region": "eu-west"}); err != nil {
		t.Fatalf("Update with the masked marker: %v", err)
	}
	if got := f.store.byID[created.ID].ExtraVars["db_pass"]; !equalJSON(got, firstCiphertext) {
		t.Fatalf("the masked marker must keep the stored ciphertext, got %#v", got)
	}
	// An explicit keep marker naming the field does the same.
	if _, err := update(map[string]any{"db_pass": map[string]any{"$secret": "keep", "from": "db_pass"}, "region": "eu-west"}); err != nil {
		t.Fatalf("Update with a keep marker: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-first" {
		t.Fatalf("after keep: %q", got)
	}
	// A new plaintext value replaces it, sealed again.
	if _, err := update(map[string]any{"db_pass": "hunter2-second", "region": "eu-west"}); err != nil {
		t.Fatalf("Update with a new value: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-second" {
		t.Fatalf("after replace: %q", got)
	}
}

func TestSecretExtraVarMarkersNeedAStoredSecret(t *testing.T) {
	f := newFixture()
	in := f.in()
	in.ExtraVarsSchema = secretSchema()
	in.ExtraVars = map[string]any{"db_pass": schema.Mask()}
	var ve *schema.ValidationError
	if _, err := f.svc.Create(ctx, f.org, in); !errors.As(err, &ve) {
		t.Fatalf("Create with a marker and nothing stored = %v, want a validation error", err)
	}
	if len(f.store.byID) != 0 {
		t.Fatal("nothing may be stored")
	}
}

// revertToLegacy overwrites the stored row with the shape a template saved
// before sealing shipped holds: plaintext under the secret-typed fields,
// nothing sealed. The service cannot produce this shape anymore — it is
// exactly what pre-sealing rows have in the database.
func revertToLegacy(t *testing.T, f fixture, tmpl templates.Template, vars map[string]any) {
	t.Helper()
	legacy := f.store.byID[tmpl.ID]
	legacy.ExtraVars = vars
	f.store.byID[tmpl.ID] = legacy
}

// TestLegacyPlaintextSecretsAreKeptAndSealedOnSave (backlog 2.47, 2.52): a
// keep or mask marker over a stored PLAINTEXT value (a template saved
// before 2.34's sealing shipped) is keepable — the save resolves the marker
// to the stored plaintext and seals it fresh with the row's AAD, so the
// legacy template saves cleanly and is sealed from that save on. Re-entered
// plaintext with no marker stays the normal fresh seal.
func TestLegacyPlaintextSecretsAreKeptAndSealedOnSave(t *testing.T) {
	f := newFixture()
	box := testBox()
	in := f.in()
	in.ExtraVarsSchema = secretSchema()
	in.ExtraVars = map[string]any{"db_pass": "hunter2-first", "region": "eu-central"}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	legacyVars := func() {
		revertToLegacy(t, f, created, map[string]any{"db_pass": "hunter2-legacy", "region": "eu-central"})
	}
	update := func(vars map[string]any) error {
		_, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
			Name: in.Name, PlaybookID: in.PlaybookID, ExtraVarsSchema: secretSchema(), ExtraVars: vars,
		})
		return err
	}
	wantMaskedRead := func(stage string) {
		t.Helper()
		got, err := f.svc.Get(ctx, f.org, created.ID)
		if err != nil {
			t.Fatalf("%s: Get: %v", stage, err)
		}
		if !equalJSON(got.ExtraVars["db_pass"], schema.Mask()) {
			t.Fatalf("%s: read shows db_pass %#v, want the mask marker", stage, got.ExtraVars["db_pass"])
		}
	}

	// The masked marker a read of the legacy row returns keeps the stored
	// plaintext, and the save seals it: the row is masked on reads and the
	// store holds a sealed envelope of the legacy value from now on.
	legacyVars()
	if err := update(map[string]any{"db_pass": schema.Mask(), "region": "eu-west"}); err != nil {
		t.Fatalf("Update with the masked marker over legacy plaintext: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-legacy" {
		t.Fatalf("after mask-marker save the sealed value opens to %q, want the legacy plaintext", got)
	}
	wantMaskedRead("after mask-marker save")
	if got := f.store.byID[created.ID].ExtraVars["region"]; got != "eu-west" {
		t.Fatalf("a string field keeps storing plainly: %#v", got)
	}

	// An explicit keep marker does the same on a fresh legacy row.
	legacyVars()
	if err := update(map[string]any{"db_pass": map[string]any{"$secret": "keep", "from": "db_pass"}, "region": "eu-west"}); err != nil {
		t.Fatalf("Update with a keep marker over legacy plaintext: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-legacy" {
		t.Fatalf("after keep-marker save the sealed value opens to %q, want the legacy plaintext", got)
	}

	// Re-entered plaintext with no marker is the normal fresh seal it has
	// always been.
	legacyVars()
	if err := update(map[string]any{"db_pass": "hunter2-new", "region": "eu-west"}); err != nil {
		t.Fatalf("Update with re-entered plaintext: %v", err)
	}
	if got := openStored(t, box, f, created, "db_pass"); got != "hunter2-new" {
		t.Fatalf("after re-entry the sealed value opens to %q, want the new plaintext", got)
	}
	wantMaskedRead("after re-entry")
}

// TestLegacyNestedPlaintextSecretIsKeptAndSealedOnSave: the legacy plaintext
// sits wherever the marker's path points — inside a repeater item too, the
// shape legacy vhosts-style schemas have.
func TestLegacyNestedPlaintextSecretIsKeptAndSealedOnSave(t *testing.T) {
	f := newFixture()
	box := testBox()
	vhosts := &schema.Schema{Fields: []schema.Field{
		{Name: "vhosts", Type: schema.TypeRepeater, Fields: []schema.Field{
			{Name: "domain", Type: schema.TypeString},
			{Name: "password", Type: schema.TypeSecret},
		}},
	}}
	in := f.in()
	in.ExtraVarsSchema = vhosts
	in.ExtraVars = map[string]any{"vhosts": []any{
		map[string]any{"domain": "a.io", "password": "pw-first"},
	}}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	revertToLegacy(t, f, created, map[string]any{"vhosts": []any{
		map[string]any{"domain": "a.io", "password": "pw-a"},
		map[string]any{"domain": "b.io", "password": "pw-b"},
	}})
	if _, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
		Name: in.Name, PlaybookID: in.PlaybookID, ExtraVarsSchema: vhosts,
		ExtraVars: map[string]any{"vhosts": []any{
			map[string]any{"domain": "a.io", "password": map[string]any{"$secret": "keep", "from": "vhosts[0].password"}},
			map[string]any{"domain": "b.io", "password": schema.Mask()},
		}},
	}); err != nil {
		t.Fatalf("Update with markers over nested legacy plaintext: %v", err)
	}
	stored := f.store.byID[created.ID].ExtraVars["vhosts"].([]any)
	for i, want := range []string{"pw-a", "pw-b"} {
		item := stored[i].(map[string]any)
		ct, ok := schema.SealedBytes(item["password"])
		if !ok {
			t.Fatalf("vhosts[%d].password = %#v, want a sealed envelope", i, item["password"])
		}
		plain, err := box.OpenAAD(ct, templates.ExtraVarsAAD(created.OrgID, created.ID))
		if err != nil {
			t.Fatalf("open vhosts[%d].password: %v", i, err)
		}
		if string(plain) != want {
			t.Fatalf("vhosts[%d].password opens to %q, want %q", i, string(plain), want)
		}
	}
}

// TestLegacyPlaintextMarkersNeedAStringOrSealedStoredValue: a marker has
// something to keep only when the stored value is a non-empty string (the
// legacy plaintext) or a sealed envelope. A marker with NO stored value at
// all, an empty string (no secret stored), or a value of any other shape —
// e.g. the number a string→secret schema change leaves behind — stays the
// validation problem it already was (backlog 2.52).
func TestLegacyPlaintextMarkersNeedAStringOrSealedStoredValue(t *testing.T) {
	cases := []struct {
		name   string
		stored map[string]any
	}{
		{"nothing stored", map[string]any{"region": "eu-central"}},
		{"empty string stored", map[string]any{"db_pass": "", "region": "eu-central"}},
		{"number stored", map[string]any{"db_pass": float64(42), "region": "eu-central"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			in := f.in()
			in.ExtraVarsSchema = secretSchema()
			in.ExtraVars = map[string]any{"region": "eu-central"}
			created, err := f.svc.Create(ctx, f.org, in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			revertToLegacy(t, f, created, tc.stored)
			var ve *schema.ValidationError
			if _, err := f.svc.Update(ctx, f.org, created.ID, templates.UpdateInput{
				Name: "deploy-site", PlaybookID: f.playbook, ExtraVarsSchema: secretSchema(),
				ExtraVars: map[string]any{"db_pass": schema.Mask(), "region": "eu-west"},
			}); !errors.As(err, &ve) || len(ve.Problems) != 1 ||
				ve.Problems[0].Path != "db_pass" || !strings.Contains(ve.Problems[0].Message, "enter it again") {
				t.Fatalf("Update = %v, want one db_pass problem telling the operator to enter the secret again", err)
			}
		})
	}
}

// testBox is the fixed sealing box the template tests share.
func testBox() *crypto.Box { return crypto.NewBox(testBoxKey("templates-extra-vars-sealing")) }

func equalJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// TestConnectionSecurityExtraVarsAreRefused (re-review N1): extra vars have
// the highest precedence in a run, so a template must not be able to switch
// host key checking off or replace the SSH arguments that enforce a pin —
// with or without a schema.
func TestConnectionSecurityExtraVarsAreRefused(t *testing.T) {
	for _, vars := range []map[string]any{
		{"ansible_ssh_args": "-o StrictHostKeyChecking=no"},
		{"ansible_host_key_checking": false},
		{"ansible_ssh_common_args": "-o UserKnownHostsFile=/tmp/x"},
		{"pulsix_ssh_common_args": ""},
	} {
		f := newFixture()
		in := f.in()
		in.ExtraVars = vars
		_, err := f.svc.Create(ctx, f.org, in)
		var ve *schema.ValidationError
		if !errors.As(err, &ve) || len(ve.Problems) != 1 {
			t.Fatalf("Create with %v = %v, want one validation problem", vars, err)
		}
		if len(f.store.byID) != 0 {
			t.Fatalf("a template with %v was stored", vars)
		}
	}
}
