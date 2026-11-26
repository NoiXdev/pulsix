package secretshadow_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/secretshadow"
	"github.com/noixdev/pulsix/internal/varsource"
)

// fakeInventory is the inventory's secret names; it records the hosts it
// is asked about.
type fakeInventory struct {
	names map[string]bool
	err   error
	asked [][]uuid.UUID
}

func (f *fakeInventory) SecretVarNames(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error) {
	f.asked = append(f.asked, slices.Clone(hostIDs))
	return f.names, f.err
}

// fakePlaybooks is a playbook's recorded variable source: an editor
// playbook's unless repo is set, a repository playbook's then, with the
// repository's vault names (nil: not reported) and the playbook's
// unreadable names.
type fakePlaybooks struct {
	fields     []schema.Field
	err        error
	repo       bool
	vault      *varsource.Vault
	unreadable []string
	asked      int
}

func (f *fakePlaybooks) VarSource(context.Context, uuid.UUID, uuid.UUID) (playbooks.VarSource, error) {
	f.asked++
	src := playbooks.VarSource{Kind: playbooks.VarSourceInline, Found: true, Playbook: varsource.Playbook{Fields: f.fields, Unreadable: f.unreadable}}
	if f.repo {
		repoID := uuid.Must(uuid.NewV7())
		src.Kind, src.RepoID, src.WorkerReads, src.Vault = playbooks.VarSourceRepo, &repoID, true, f.vault
	}
	return src, f.err
}

func known(names ...string) *varsource.Vault {
	v := &varsource.Vault{Names: []varsource.VaultName{}, EncryptedFiles: []string{}, Unread: []string{}}
	for _, n := range names {
		v.Names = append(v.Names, varsource.VaultName{Name: n, File: "group_vars/all.yml"})
	}
	return v
}

func TestCheckRefusesAShadowedName(t *testing.T) {
	ctx, org, pb, host := context.Background(), uuid.New(), uuid.New(), uuid.New()
	cut := known()
	cut.Cut = true
	encrypted := known()
	encrypted.EncryptedFiles = []string{"group_vars/all/vault.yml"}
	for _, tc := range []struct {
		name string
		inv  map[string]bool
		pb   fakePlaybooks
		msg  string // "" passes
	}{
		{"nothing secret", nil, fakePlaybooks{}, ""},
		{"inventory secret", map[string]bool{"version": true}, fakePlaybooks{},
			"extra var version: the inventory holds a secret of that name for a target host, which an extra var must not replace"},
		{"playbook secret", nil, fakePlaybooks{fields: []schema.Field{{Name: "version", Type: schema.TypeSecret}}},
			"extra var version: the playbook declares a secret of that name, which an extra var must not replace"},
		{"playbook secret inside an object", nil, fakePlaybooks{fields: []schema.Field{{Name: "version", Type: schema.TypeObject, Fields: []schema.Field{{Name: "key", Type: schema.TypeSecret}}}}},
			"the playbook declares a secret"},
		{"vault name", nil, fakePlaybooks{repo: true, vault: known("other", "version")},
			"extra var version: the repository sets a variable of that name to a value encrypted with Ansible Vault (group_vars/all.yml), which an extra var must not replace"},
		{"vault name, other names only", nil, fakePlaybooks{repo: true, vault: known("other")}, ""},
		{"names unknown (older worker), name unreadable", nil, fakePlaybooks{repo: true, unreadable: []string{"version"}},
			"extra var version: the playbook declares a variable of that name whose value could not be read, and the repository's vault-encrypted variables are not known (sync the repository with an updated worker); an extra var must not replace it"},
		{"names unknown (older worker), name readable", nil, fakePlaybooks{repo: true, unreadable: []string{"other"}}, ""},
		{"names cut, name unreadable", nil, fakePlaybooks{repo: true, vault: cut, unreadable: []string{"version"}}, "not known"},
		{"file encrypted whole, name unreadable", nil, fakePlaybooks{repo: true, vault: encrypted, unreadable: []string{"version"}}, "not known"},
		{"names known, name unreadable", nil, fakePlaybooks{repo: true, vault: known(), unreadable: []string{"version"}}, ""},
		{"editor playbook skips the vault", nil, fakePlaybooks{unreadable: []string{"version"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := &fakeInventory{names: tc.inv}
			pbs := tc.pb
			c := secretshadow.Checker{Inventory: inv, Playbooks: &pbs}
			err := c.Check(ctx, org, pb, []uuid.UUID{host}, []string{"version"}, "extra var")
			if tc.msg == "" {
				if err != nil {
					t.Fatalf("check = %v, want it to pass", err)
				}
				return
			}
			if !errors.Is(err, secretshadow.ErrShadow) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("check = %v, want ErrShadow with %q", err, tc.msg)
			}
			var r *secretshadow.Refusal
			if !errors.As(err, &r) || r.Name != "version" {
				t.Fatalf("check = %v, want a Refusal naming version", err)
			}
			if len(inv.asked) != 1 || !slices.Equal(inv.asked[0], []uuid.UUID{host}) {
				t.Fatalf("inventory asked about %v, want the host", inv.asked)
			}
		})
	}
}

// The noun names what is checked; "a"/"an" follows it, and the first name
// in sorted order is refused.
func TestCheckNamesWhatAndTheFirstName(t *testing.T) {
	c := secretshadow.Checker{Inventory: &fakeInventory{names: map[string]bool{"b": true, "c": true}}, Playbooks: &fakePlaybooks{}}
	err := c.Check(context.Background(), uuid.New(), uuid.New(), nil, []string{"c", "a", "b"}, "mapping target")
	want := "mapping target b: the inventory holds a secret of that name for a target host, which a mapping target must not replace"
	if err == nil || err.Error() != want {
		t.Fatalf("check = %v, want %q", err, want)
	}
}

func TestCheckReadsOnlyWhatItNeeds(t *testing.T) {
	ctx, org := context.Background(), uuid.New()
	// No names: nothing is read, and missing sources do not matter.
	inv, pbs := &fakeInventory{}, &fakePlaybooks{}
	if err := (secretshadow.Checker{Inventory: inv, Playbooks: pbs}).Check(ctx, org, uuid.New(), nil, nil, "extra var"); err != nil || len(inv.asked) != 0 || pbs.asked != 0 {
		t.Fatalf("empty names = %v, asked %v / %d", err, inv.asked, pbs.asked)
	}
	if err := (secretshadow.Checker{}).Check(ctx, org, uuid.New(), nil, []string{}, "extra var"); err != nil {
		t.Fatalf("empty names without sources = %v", err)
	}
	// No playbook: only the inventory is asked.
	inv, pbs = &fakeInventory{}, &fakePlaybooks{fields: []schema.Field{{Name: "version", Type: schema.TypeSecret}}}
	if err := (secretshadow.Checker{Inventory: inv, Playbooks: pbs}).Check(ctx, org, uuid.Nil, nil, []string{"version"}, "extra var"); err != nil || len(inv.asked) != 1 || pbs.asked != 0 {
		t.Fatalf("no playbook = %v, asked %v / %d", err, inv.asked, pbs.asked)
	}
}

func TestCheckFailsClosed(t *testing.T) {
	ctx, org, names := context.Background(), uuid.New(), []string{"version"}
	for _, c := range []secretshadow.Checker{{}, {Inventory: &fakeInventory{}}, {Playbooks: &fakePlaybooks{}}} {
		if err := c.Check(ctx, org, uuid.New(), nil, names, "extra var"); !errors.Is(err, secretshadow.ErrNoSources) {
			t.Fatalf("check without sources = %v, want ErrNoSources", err)
		}
	}
	boom := errors.New("boom")
	err := (secretshadow.Checker{Inventory: &fakeInventory{err: boom}, Playbooks: &fakePlaybooks{}}).Check(ctx, org, uuid.New(), nil, names, "extra var")
	if !errors.Is(err, boom) || errors.Is(err, secretshadow.ErrShadow) || err.Error() != "read the inventory's secret names: boom" {
		t.Fatalf("inventory failure = %v", err)
	}
	err = (secretshadow.Checker{Inventory: &fakeInventory{}, Playbooks: &fakePlaybooks{err: playbooks.ErrNotFound}}).Check(ctx, org, uuid.New(), nil, names, "extra var")
	if !errors.Is(err, playbooks.ErrNotFound) || !strings.HasPrefix(err.Error(), "read the playbook's variable source: ") {
		t.Fatalf("playbook failure = %v", err)
	}
}
