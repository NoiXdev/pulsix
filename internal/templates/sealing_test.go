// Secret-typed extra vars are sealed at rest (final review triage, Task 7's
// hardening item): a template's secret values never sit in the database as
// plaintext. Create and Update seal them with the template's own AAD; an
// Update that sends back the masked marker (the value a read returned) or
// an explicit keep marker keeps the stored ciphertext; the claim path opens
// them again (internal/app's template lookup).
package templates_test

import (
	"bytes"
	"encoding/json"
	"errors"
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
