package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/varsource"
)

// fakeInventorySecrets is the inventory's secret names for the hosts it is
// asked about; it records the hosts.
type fakeInventorySecrets struct {
	names map[string]bool
	asked [][]uuid.UUID
}

func (f *fakeInventorySecrets) SecretVarNames(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error) {
	f.asked = append(f.asked, slices.Clone(hostIDs))
	return f.names, nil
}

// fakePlaybookVars is the template's playbook's recorded variable source.
type fakePlaybookVars struct {
	fields []schema.Field
	err    error
}

func (f *fakePlaybookVars) VarSource(context.Context, uuid.UUID, uuid.UUID) (playbooks.VarSource, error) {
	return playbooks.VarSource{Found: true, Playbook: varsource.Playbook{Fields: f.fields}}, f.err
}

// TestOverridesMayNotReplaceASecret (final review I2, controller ruling):
// secrets are never set by the assistant, so an override var is refused
// on a template that declares no extra vars at all, and under any name the
// run's variable sources hold a secret by: the template's schema, the
// inventory groups of the run's hosts, and the variables the playbook
// declares secret. The value is never echoed.
func TestOverridesMayNotReplaceASecret(t *testing.T) {
	ctx, org := context.Background(), id()
	for _, tc := range []struct {
		name string
		prep func(w *world)
		msg  string
	}{
		{"template without a schema", noSchema, "declares no extra variables"},
		{"template secret", func(w *world) {
			tmpl := w.tmpls.fakeTemplates[w.x]
			tmpl.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "version", Type: schema.TypeSecret}}}
			w.tmpls.fakeTemplates[w.x] = tmpl
		}, "secret"},
		{"inventory secret", func(w *world) { w.invSecrets.names["version"] = true }, "inventory"},
		{"playbook secret", func(w *world) {
			w.pbVars.fields = []schema.Field{{Name: "version", Type: schema.TypeSecret}}
		}, "playbook"},
		{"playbook secret inside an object", func(w *world) {
			w.pbVars.fields = []schema.Field{{Name: "version", Type: schema.TypeObject, Fields: []schema.Field{{Name: "key", Type: schema.TypeSecret}}}}
		}, "playbook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tc.prep(w)
			_, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{ExtraVars: map[string]any{"version": "s3cret-value-9f"}})
			if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), tc.msg) || !strings.Contains(err.Error(), "version") {
				t.Fatalf("validate = %v, want ErrInvalidInput naming version and %q", err, tc.msg)
			}
			if strings.Contains(err.Error(), "s3cret-value-9f") {
				t.Fatalf("the refusal echoes the value: %v", err)
			}
		})
	}

	// The inventory is asked about exactly the hosts the run keeps to.
	w := newWorld()
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{ExtraVars: map[string]any{"version": "1"}, HostIDs: []uuid.UUID{w.h2}}); err != nil {
		t.Fatal(err)
	}
	if len(w.invSecrets.asked) != 1 || !slices.Equal(w.invSecrets.asked[0], []uuid.UUID{w.h2}) {
		t.Fatalf("inventory asked about %v, want h2", w.invSecrets.asked)
	}
	// Without extra vars nothing is looked up.
	w = newWorld()
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{Check: true}); err != nil || len(w.invSecrets.asked) != 0 {
		t.Fatalf("dry run without vars = %v, asked %v", err, w.invSecrets.asked)
	}
	// A secret that appears after the request refuses the approval.
	w = newWorld()
	w.invSecrets.names["version"] = true
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, Overrides: &runrequests.Overrides{ExtraVars: map[string]any{"version": "1"}}}
	if err := w.resolver().Run(ctx, org, req, id(), "k", id(), nil); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("run over an inventory secret = %v", err)
	}
}
