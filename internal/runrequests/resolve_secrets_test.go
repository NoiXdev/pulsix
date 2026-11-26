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

// fakePlaybookVars is the template's playbook's recorded variable source:
// an inline one unless repo is set, a repository playbook's then, with
// the repository's vault names (nil: not reported) and the playbook's
// unreadable names.
type fakePlaybookVars struct {
	fields     []schema.Field
	err        error
	repo       bool
	vault      *varsource.Vault
	unreadable []string
}

func (f *fakePlaybookVars) VarSource(context.Context, uuid.UUID, uuid.UUID) (playbooks.VarSource, error) {
	src := playbooks.VarSource{Kind: playbooks.VarSourceInline, Found: true, Playbook: varsource.Playbook{Fields: f.fields, Unreadable: f.unreadable}}
	if f.repo {
		repoID := uuid.Must(uuid.NewV7())
		src.Kind, src.RepoID, src.WorkerReads, src.Vault = playbooks.VarSourceRepo, &repoID, true, f.vault
	}
	return src, f.err
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

// TestOverridesMayNotReplaceAVaultVariable (backlog 2.134): an override
// named like a variable the repository sets to a vault-encrypted value is
// refused, naming the file but never a value. When the repository's last
// read did not report the names (an older worker, a sync before the
// upgrade) or did not read every file, the check cannot know them: it
// refuses a name the playbook declares but could not read, and passes the
// rest.
func TestOverridesMayNotReplaceAVaultVariable(t *testing.T) {
	ctx, org := context.Background(), id()
	known := func(names ...string) *varsource.Vault {
		v := &varsource.Vault{Names: []varsource.VaultName{}, EncryptedFiles: []string{}, Unread: []string{}}
		for _, n := range names {
			v.Names = append(v.Names, varsource.VaultName{Name: n, File: "group_vars/all.yml"})
		}
		return v
	}
	cut := known()
	cut.Cut = true
	encrypted := known()
	encrypted.EncryptedFiles = []string{"group_vars/all/vault.yml"}
	for _, tc := range []struct {
		name string
		pb   fakePlaybookVars
		msg  string // "" passes
	}{
		{"vault name", fakePlaybookVars{repo: true, vault: known("other", "version")}, "Ansible Vault (group_vars/all.yml)"},
		{"vault name, other names only", fakePlaybookVars{repo: true, vault: known("other")}, ""},
		{"names unknown (older worker), name unreadable", fakePlaybookVars{repo: true, unreadable: []string{"version"}}, "not known"},
		{"names unknown (older worker), name readable", fakePlaybookVars{repo: true, unreadable: []string{"other"}}, ""},
		{"names cut, name unreadable", fakePlaybookVars{repo: true, vault: cut, unreadable: []string{"version"}}, "not known"},
		{"file encrypted whole, name unreadable", fakePlaybookVars{repo: true, vault: encrypted, unreadable: []string{"version"}}, "not known"},
		{"names known, name unreadable", fakePlaybookVars{repo: true, vault: known(), unreadable: []string{"version"}}, ""},
		{"editor playbook", fakePlaybookVars{unreadable: []string{"version"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			*w.pbVars = tc.pb
			_, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{ExtraVars: map[string]any{"version": "s3cret-value-9f"}})
			if tc.msg == "" {
				if err != nil {
					t.Fatalf("validate = %v, want it to pass", err)
				}
				return
			}
			if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), tc.msg) || !strings.Contains(err.Error(), "version") {
				t.Fatalf("validate = %v, want ErrInvalidInput naming version and %q", err, tc.msg)
			}
			if strings.Contains(err.Error(), "s3cret-value-9f") {
				t.Fatalf("the refusal echoes the value: %v", err)
			}
		})
	}
}
