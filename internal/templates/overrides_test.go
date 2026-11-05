package templates_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

func strptr(s string) *string { return &s }

// overrideFixture stores a template with a schema: region (required string,
// satisfied by the template), replicas (int), token (secret, stored sealed).
func overrideFixture(t *testing.T) (fixture, templates.Template) {
	t.Helper()
	f := newFixture()
	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "region", Type: schema.TypeString, Required: true},
		{Name: "replicas", Type: schema.TypeInt},
		{Name: "token", Type: schema.TypeSecret},
	}}
	tmpl, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name: "deploy", PlaybookID: f.playbook, ExtraVarsSchema: sc,
		ExtraVars: map[string]any{"region": "eu", "token": "template-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, tmpl
}

func problemPaths(err error) []string {
	var ve *schema.ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	out := make([]string, len(ve.Problems))
	for i, p := range ve.Problems {
		out[i] = p.Path
	}
	return out
}

func TestValidateOverridesMergesOverTheTemplate(t *testing.T) {
	f, tmpl := overrideFixture(t)
	// region stays satisfied by the template; replicas is overridden.
	got, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		Limit: strptr("web*"), ExtraVars: map[string]any{"replicas": float64(3)},
	}, templates.OverrideOptions{})
	if err != nil {
		t.Fatalf("ValidateOverrides: %v", err)
	}
	if *got.Limit != "web*" || got.ExtraVars["replicas"] != float64(3) || len(got.ExtraVars) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateOverridesRejects(t *testing.T) {
	f, tmpl := overrideFixture(t)
	cases := []struct {
		name  string
		in    templates.Overrides
		paths []string
	}{
		{"undeclared var", templates.Overrides{ExtraVars: map[string]any{"colour": "red"}}, []string{"colour"}},
		{"wrong type", templates.Overrides{ExtraVars: map[string]any{"replicas": "three"}}, []string{"replicas"}},
		{"required var cleared", templates.Overrides{ExtraVars: map[string]any{"region": nil}}, []string{"region"}},
		{"connection security var", templates.Overrides{ExtraVars: map[string]any{"ansible_ssh_common_args": "-o X"}}, []string{"ansible_ssh_common_args"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, c.in, templates.OverrideOptions{})
			if got := problemPaths(err); strings.Join(got, ",") != strings.Join(c.paths, ",") {
				t.Fatalf("err = %v, problem paths %v, want %v", err, got, c.paths)
			}
		})
	}
	if _, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{Limit: strptr(strings.Repeat("a", 501))}, templates.OverrideOptions{}); !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("long limit: %v", err)
	}
	if _, err := f.svc.ValidateOverrides(ctx, f.org, uuid.Must(uuid.NewV7()), templates.Overrides{}, templates.OverrideOptions{}); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown template: %v", err)
	}
}

func TestValidateOverridesSealsAndRevealsSecrets(t *testing.T) {
	f, tmpl := overrideFixture(t)
	aad := []byte("schedule:test")
	stored, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"token": "override-secret"},
	}, templates.OverrideOptions{SealAAD: aad})
	if err != nil {
		t.Fatal(err)
	}
	if _, sealed := schema.SealedBytes(stored.ExtraVars["token"]); !sealed {
		t.Fatalf("token = %#v, want a sealed envelope", stored.ExtraVars["token"])
	}
	// A later save that keeps the secret (the masked marker a read returned)
	// resolves against the stored override, not the template.
	kept, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"token": schema.Mask()},
	}, templates.OverrideOptions{SealAAD: aad, Stored: stored.ExtraVars})
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := f.svc.RevealOverrides(ctx, f.org, tmpl.ID, kept, aad)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.ExtraVars["token"] != "override-secret" {
		t.Fatalf("revealed = %#v", revealed.ExtraVars)
	}
	// The wrong AAD never opens it.
	if _, err := f.svc.RevealOverrides(ctx, f.org, tmpl.ID, kept, []byte("schedule:other")); err == nil {
		t.Fatal("RevealOverrides opened a value sealed for another schedule")
	}
}

func TestValidateOverridesRefusesSecretsForWebhooks(t *testing.T) {
	f, tmpl := overrideFixture(t)
	_, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"token": "from-a-payload"},
	}, templates.OverrideOptions{RefuseSecrets: true})
	if got := problemPaths(err); len(got) != 1 || got[0] != "token" {
		t.Fatalf("err = %v", err)
	}
}

// TestValidateOverridesStrictLimitRefusesFileAndRegexPatterns is final
// review C1: a limit mapped from a webhook payload must not make Ansible
// read a file on the worker ("@/path") or use any other pattern syntax
// beyond host and group names.
func TestValidateOverridesStrictLimitRefusesFileAndRegexPatterns(t *testing.T) {
	f, tmpl := overrideFixture(t)
	for _, limit := range []string{"@/etc/passwd", "web,@/root/.ssh/id_ed25519", "~web.*", "web[0]", "{{ x }}"} {
		_, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{Limit: strptr(limit)},
			templates.OverrideOptions{RefuseSecrets: true, StrictLimit: true})
		if !errors.Is(err, templates.ErrInvalidInput) {
			t.Errorf("limit %q: err = %v, want ErrInvalidInput", limit, err)
		}
		if err != nil && strings.Contains(err.Error(), limit) {
			t.Errorf("the refusal must not echo the value: %v", err)
		}
	}
	got, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{Limit: strptr("web*:&staging:!db-01")},
		templates.OverrideOptions{RefuseSecrets: true, StrictLimit: true})
	if err != nil || *got.Limit != "web*:&staging:!db-01" {
		t.Fatalf("a plain host pattern must pass: %+v, %v", got, err)
	}
	// Operators (schedules) keep the full pattern syntax.
	if _, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{Limit: strptr("~web.*")}, templates.OverrideOptions{}); err != nil {
		t.Fatalf("an operator's limit keeps the full syntax: %v", err)
	}
}

func TestValidateOverridesWithoutSchemaOnlyAppliesTheDenylist(t *testing.T) {
	f := newFixture()
	tmpl, err := f.svc.Create(ctx, f.org, f.in())
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{ExtraVars: map[string]any{"anything": []any{"a"}}}, templates.OverrideOptions{})
	if err != nil || got.ExtraVars["anything"] == nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{ExtraVars: map[string]any{"PULSIX_x": 1}}, templates.OverrideOptions{}); problemPaths(err) == nil {
		t.Fatalf("denylisted prefix accepted: %v", err)
	}
}

func TestSecretFields(t *testing.T) {
	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "plain", Type: schema.TypeString},
		{Name: "pw", Type: schema.TypeSecret},
		{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{{Name: "password", Type: schema.TypeSecret}}},
		{Name: "keys", Type: schema.TypeList, Item: &schema.Field{Type: schema.TypeSecret}},
	}}
	got := templates.SecretFields(sc)
	if got["plain"] || !got["pw"] || !got["db"] || !got["keys"] {
		t.Fatalf("SecretFields = %v", got)
	}
	if len(templates.SecretFields(nil)) != 0 {
		t.Fatal("nil schema has no secret fields")
	}
}

func TestOverridesEmpty(t *testing.T) {
	if !(templates.Overrides{}).Empty() || (templates.Overrides{Limit: strptr("")}).Empty() {
		t.Fatal("Empty is wrong")
	}
}

// TestOverridesIgnoreInactiveFields (spec 7.75 goal 3): an override is
// checked like a save. A value under an inactive field is accepted and not
// checked, an inactive required field may stay empty, and a value the
// template keeps under a removed name does not make the merge "unknown";
// an active required field without a value is still refused.
func TestOverridesIgnoreInactiveFields(t *testing.T) {
	f := newFixture()
	sc := &schema.Schema{Fields: []schema.Field{
		{Name: "region", Type: schema.TypeString, Required: true},
		{Name: "replicas", Type: schema.TypeInt},
		{Name: "legacy", Type: schema.TypeString},
	}}
	tmpl, err := f.svc.Create(ctx, f.org, templates.CreateInput{
		Name: "deploy", PlaybookID: f.playbook, ExtraVarsSchema: sc,
		ExtraVars: map[string]any{"region": "eu", "legacy": "kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// legacy is removed (its stored value stays), gate is inactive and
	// required without a value, replicas inactive.
	if _, err := f.svc.UpdateVariables(ctx, f.org, tmpl.ID, templates.VariablesInput{
		Schema: &schema.Schema{Fields: []schema.Field{
			{Name: "region", Type: schema.TypeString, Required: true},
			{Name: "replicas", Type: schema.TypeInt, Active: inactive()},
			{Name: "gate", Type: schema.TypeString, Required: true, Active: inactive()},
		}},
		ExtraVars: map[string]any{"region": "eu"},
	}); err != nil {
		t.Fatalf("UpdateVariables: %v", err)
	}
	if f.store.byID[tmpl.ID].ExtraVars["legacy"] != "kept" {
		t.Fatalf("the removed field's value must stay stored: %#v", f.store.byID[tmpl.ID].ExtraVars)
	}

	got, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"replicas": "not-a-number"},
	}, templates.OverrideOptions{})
	if err != nil {
		t.Fatalf("override of an inactive field: %v", err)
	}
	if got.ExtraVars["replicas"] != "not-a-number" {
		t.Fatalf("override = %+v", got)
	}
	if _, err := f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"legacy": "x"},
	}, templates.OverrideOptions{}); !strings.Contains(strings.Join(problemPaths(err), ","), "legacy") {
		t.Fatalf("an override naming a removed field = %v, want an unknown field problem", err)
	}

	stored := f.store.byID[tmpl.ID]
	delete(stored.ExtraVars, "region")
	_, err = f.svc.ValidateOverrides(ctx, f.org, tmpl.ID, templates.Overrides{
		ExtraVars: map[string]any{"replicas": float64(2)},
	}, templates.OverrideOptions{})
	var ve *schema.ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 1 || ve.Problems[0].Path != "region" || ve.Problems[0].Message != "is required" {
		t.Fatalf("active required field without a value = %v, want exactly region: is required", err)
	}
}
