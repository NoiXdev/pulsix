package triggers_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/secretshadow"
	"github.com/noixdev/pulsix/internal/triggers"
	"github.com/noixdev/pulsix/internal/varsource"
)

// fakeInvSecrets is the inventory's secret names, held for any host it is
// asked about (none for no host, as inventory.Service); it records the
// hosts.
type fakeInvSecrets struct {
	mu    sync.Mutex
	names map[string]bool
	asked [][]uuid.UUID
}

func (f *fakeInvSecrets) SecretVarNames(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, slices.Clone(hostIDs))
	if len(hostIDs) == 0 {
		return map[string]bool{}, nil
	}
	return f.names, nil
}

func (f *fakeInvSecrets) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// fakePbVars is the template's playbook's recorded variable source: an
// editor playbook's unless repo is set, a repository playbook's then.
type fakePbVars struct {
	mu         sync.Mutex
	fields     []schema.Field
	repo       bool
	vault      *varsource.Vault
	unreadable []string
	asked      int
}

func (f *fakePbVars) VarSource(context.Context, uuid.UUID, uuid.UUID) (playbooks.VarSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	src := playbooks.VarSource{Kind: playbooks.VarSourceInline, Found: true, Playbook: varsource.Playbook{Fields: f.fields, Unreadable: f.unreadable}}
	if f.repo {
		repoID := uuid.Must(uuid.NewV7())
		src.Kind, src.RepoID, src.WorkerReads, src.Vault = playbooks.VarSourceRepo, &repoID, true, f.vault
	}
	return src, nil
}

// fakeScope is the template's hosts before any limit (jobs.RunTargets).
type fakeScope struct {
	mu    sync.Mutex
	hosts []uuid.UUID
	err   error
	asked int
}

func (f *fakeScope) RunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	return f.hosts, f.err
}

func (w *world) secrets() secretshadow.Checker {
	return secretshadow.Checker{Inventory: w.inv, Playbooks: w.pbs}
}

// noSecrets is a checker whose sources hold no secret.
func noSecrets() secretshadow.Checker {
	return secretshadow.Checker{Inventory: &fakeInvSecrets{}, Playbooks: &fakePbVars{}}
}

func vaultNames(names ...string) *varsource.Vault {
	v := &varsource.Vault{Names: []varsource.VaultName{}, EncryptedFiles: []string{}, Unread: []string{}}
	for _, n := range names {
		v.Names = append(v.Names, varsource.VaultName{Name: n, File: "group_vars/all.yml"})
	}
	return v
}

// shadowCases are secrets named "release", the template's mapped
// variable, in each of the run's other variable sources; msg "" passes.
var shadowCases = []struct {
	name string
	prep func(w *world)
	msg  string
}{
	{"inventory secret", func(w *world) { w.inv.names["release"] = true },
		"mapping target release: the inventory holds a secret of that name for a target host, which a mapping target must not replace"},
	{"playbook secret", func(w *world) { w.pbs.fields = []schema.Field{{Name: "release", Type: schema.TypeSecret}} },
		"mapping target release: the playbook declares a secret of that name"},
	{"vault variable", func(w *world) { w.pbs.repo, w.pbs.vault = true, vaultNames("release") },
		"mapping target release: the repository sets a variable of that name to a value encrypted with Ansible Vault (group_vars/all.yml)"},
	{"vault names unknown, name unreadable", func(w *world) { w.pbs.repo, w.pbs.unreadable = true, []string{"release"} },
		"the repository's vault-encrypted variables are not known"},
	{"vault names unknown, name readable", func(w *world) { w.pbs.repo, w.pbs.unreadable = true, []string{"other"} }, ""},
	{"vault names known, name unreadable", func(w *world) {
		w.pbs.repo, w.pbs.vault, w.pbs.unreadable = true, vaultNames("other"), []string{"release"}
	}, ""},
	{"secret of another name", func(w *world) { w.inv.names["other"] = true }, ""},
}

// TestSaveRefusesAMappingOntoASecret (backlog 2.139, R3): a mapping
// target named like a secret of the template's hosts (its scope before
// any limit), its playbook or its repository's vault is refused on create,
// update and validate, naming the variable and the kind of secret.
func TestSaveRefusesAMappingOntoASecret(t *testing.T) {
	for _, tc := range shadowCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			ok, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
			if err != nil {
				t.Fatal(err)
			}
			tc.prep(w)
			in := w.templateInput()
			in.Name = "another"
			_, cerr := w.svc.Create(ctx, w.org, w.actor, in)
			_, uerr := w.svc.Update(ctx, w.org, ok.Trigger.ID, ok.Trigger.Revision, w.templateInput())
			in.Name = "a third"
			_, verr := w.svc.Validate(ctx, w.org, uuid.Must(uuid.NewV7()), in)
			for op, err := range map[string]error{"create": cerr, "update": uerr, "validate": verr} {
				if tc.msg == "" {
					if err != nil {
						t.Fatalf("%s = %v, want it to pass", op, err)
					}
					continue
				}
				if !errors.Is(err, triggers.ErrInvalidInput) || !strings.Contains(err.Error(), tc.msg) {
					t.Fatalf("%s = %v, want ErrInvalidInput with %q", op, err, tc.msg)
				}
			}
		})
	}
	// The inventory is asked about the template's unlimited scope.
	w := newWorld(t)
	if _, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput()); err != nil {
		t.Fatal(err)
	}
	if len(w.inv.asked) != 1 || !slices.Equal(w.inv.asked[0], w.scope.hosts) {
		t.Fatalf("inventory asked about %v, want the scope's hosts %v", w.inv.asked, w.scope.hosts)
	}
}

// TestSaveWithoutMappedVarsReadsNoSecrets (R5): a mapping without extra
// vars, and a repo_sync trigger, read no secret source and need none.
func TestSaveWithoutMappedVarsReadsNoSecrets(t *testing.T) {
	w := newWorld(t)
	w.svc = triggers.NewService(triggers.Deps{Store: w.store, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{}})
	in := w.templateInput()
	in.Mapping = triggers.Mapping{Limit: "$.target"}
	if _, err := w.svc.Create(ctx, w.org, w.actor, in); err != nil {
		t.Fatalf("limit-only mapping without secret checks = %v", err)
	}
	if _, err := w.svc.Create(ctx, w.org, w.actor, triggers.Input{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "sync", Enabled: true, Preset: triggers.PresetGitea}); err != nil {
		t.Fatalf("repo_sync without secret checks = %v", err)
	}
	w = newWorld(t)
	in = w.templateInput()
	in.Mapping = triggers.Mapping{}
	if _, err := w.svc.Create(ctx, w.org, w.actor, in); err != nil || w.inv.calls() != 0 || w.pbs.asked != 0 || w.scope.asked != 0 {
		t.Fatalf("no mapping = %v; read inventory %d, playbook %d, scope %d", err, w.inv.calls(), w.pbs.asked, w.scope.asked)
	}
}

// TestSaveWithoutSecretChecksFailsClosed: a service wired without the
// secret checks refuses a mapping with extra vars instead of saving it
// unchecked.
func TestSaveWithoutSecretChecksFailsClosed(t *testing.T) {
	w := newWorld(t)
	for _, d := range []triggers.Deps{
		{Store: w.store, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{}},
		{Store: w.store, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{}, Secrets: w.secrets()},
		{Store: w.store, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{}, Scope: w.scope},
	} {
		svc := triggers.NewService(d)
		if _, err := svc.Create(ctx, w.org, w.actor, w.templateInput()); err == nil {
			t.Fatal("Create without secret checks passed")
		}
	}
	if len(w.store.byID) != 0 {
		t.Fatalf("stored %d triggers", len(w.store.byID))
	}
}

// TestSaveScopeThatReachesNoHost: a scope that resolves to no host now (or
// a template whose playbook is not registered) leaves the inventory with
// no host to ask about; the playbook is still checked, and every delivery
// checks again. Any other scope failure fails the save.
func TestSaveScopeThatReachesNoHost(t *testing.T) {
	for _, scopeErr := range []error{jobs.ErrEmptyScope, jobs.ErrTemplateNotReady} {
		w := newWorld(t)
		w.scope.err, w.inv.names["release"] = scopeErr, true
		if _, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput()); err != nil {
			t.Fatalf("scope %v = %v, want the save to pass", scopeErr, err)
		}
		w.pbs.fields = []schema.Field{{Name: "release", Type: schema.TypeSecret}}
		in := w.templateInput()
		in.Name = "another"
		if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, triggers.ErrInvalidInput) {
			t.Fatalf("scope %v with a playbook secret = %v, want ErrInvalidInput", scopeErr, err)
		}
	}
	w := newWorld(t)
	boom := errors.New("boom")
	w.scope.err = boom
	if _, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput()); !errors.Is(err, boom) || errors.Is(err, triggers.ErrInvalidInput) {
		t.Fatalf("scope failure = %v", err)
	}
}

// TestDeliveryRefusesAMappingOntoASecret (R2, R4): a secret that appears
// after the trigger was saved fails the delivery as a payload problem at
// extra_vars.<name>: no run starts, the delivery is recorded as
// invalid_payload, and neither its reason nor the answer echoes the value.
// Test calls take the same path.
func TestDeliveryRefusesAMappingOntoASecret(t *testing.T) {
	for _, tc := range shadowCases {
		t.Run(tc.name, func(t *testing.T) {
			rw := newReceiveWorld(t)
			c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
			if err != nil {
				t.Fatal(err)
			}
			tc.prep(rw.world)
			res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"s3cret-value-9f"}`))
			test, terr := rw.svc.TestCall(ctx, rw.org, c.Trigger.ID, []byte(`{"ref":"s3cret-value-9f"}`))
			if tc.msg == "" {
				if err != nil || res.Outcome != triggers.OutcomeTriggered || terr != nil || test.Outcome != triggers.OutcomeTriggered {
					t.Fatalf("Receive = %+v, %v; TestCall = %+v, %v; want both triggered", res, err, test, terr)
				}
				return
			}
			for _, r := range []triggers.Result{res, test} {
				if r.Outcome != triggers.OutcomeInvalidPayload || r.Field != "extra_vars.release" || r.RunID != nil {
					t.Fatalf("result = %+v (%v, %v), want invalid_payload at extra_vars.release", r, err, terr)
				}
			}
			if err != nil || terr != nil || len(rw.runs.requests) != 0 {
				t.Fatalf("errors %v, %v; runs %d", err, terr, len(rw.runs.requests))
			}
			if len(rw.rec.deliveries) != 2 {
				t.Fatalf("deliveries = %+v", rw.rec.deliveries)
			}
			for _, d := range rw.rec.deliveries {
				if d.Outcome != triggers.OutcomeInvalidPayload || !strings.Contains(d.Reason, "invalid value at extra_vars.release") ||
					!strings.Contains(d.Reason, tc.msg) || strings.Contains(d.Reason, "s3cret-value-9f") {
					t.Fatalf("delivery = %+v, want invalid_payload naming release and %q, not the value", d, tc.msg)
				}
			}
			if !strings.HasPrefix(rw.rec.deliveries[1].Reason, "test call; ") {
				t.Fatalf("test call reason = %q", rw.rec.deliveries[1].Reason)
			}
		})
	}
}

// TestDeliveryChecksOnlyTheMappedNames (R5): a delivery whose payload sets
// no mapped extra var reads no secret source.
func TestDeliveryChecksOnlyTheMappedNames(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	inv, pb, scope := rw.inv.calls(), rw.pbs.asked, rw.scope.asked
	rw.inv.names["release"] = true
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"other":"x"}`))
	if err != nil || res.Outcome != triggers.OutcomeTriggered {
		t.Fatalf("Receive = %+v, %v", res, err)
	}
	if rw.inv.calls() != inv || rw.pbs.asked != pb || rw.scope.asked != scope {
		t.Fatal("a delivery without mapped extra vars read a secret source")
	}
}

// TestDeliveryWithoutSecretChecksFailsClosed: a trigger stored with a
// mapping (before this check, or on a service wired without it) is never
// delivered unchecked.
func TestDeliveryWithoutSecretChecksFailsClosed(t *testing.T) {
	rw := newReceiveWorld(t)
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, rw.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	rw.svc = triggers.NewService(triggers.Deps{Store: rw.rec, Templates: rw.tmpls, Repos: rw.repos, Box: fakeBox{}, Runs: rw.runs, Audit: rw.audit})
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`))
	if err == nil || res.Outcome == triggers.OutcomeTriggered || len(rw.runs.requests) != 0 {
		t.Fatalf("Receive without secret checks = %+v, %v; runs %d", res, err, len(rw.runs.requests))
	}
}

// TestSettlingDeliveryOntoASecretLeavesThePendingRun (2.137): a refused
// delivery to a trigger with a settle delay is not collected, so it
// neither moves the quiet window nor replaces the collected values.
func TestSettlingDeliveryOntoASecretLeavesThePendingRun(t *testing.T) {
	rw := newReceiveWorld(t)
	p := &fakePending{}
	rw.withPending(p)
	in := rw.templateInput()
	in.SettleSeconds = 30
	c, err := rw.svc.Create(ctx, rw.org, rw.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"v1"}`)); err != nil || res.Outcome != triggers.OutcomeCollected {
		t.Fatalf("first delivery = %+v, %v", res, err)
	}
	rw.inv.names["release"] = true
	res, err := rw.svc.Receive(ctx, rw.signed(t, c, `{"ref":"s3cret-value-9f"}`))
	if err != nil || res.Outcome != triggers.OutcomeInvalidPayload || res.Field != "extra_vars.release" || res.PendingID != nil {
		t.Fatalf("refused delivery = %+v, %v", res, err)
	}
	if len(p.collects) != 1 || len(rw.runs.requests) != 0 {
		t.Fatalf("collects %d, runs %d: the refused delivery touched the pending run", len(p.collects), len(rw.runs.requests))
	}
	if len(rw.rec.deliveries) != 1 || rw.rec.deliveries[0].Outcome != triggers.OutcomeInvalidPayload {
		t.Fatalf("deliveries = %+v", rw.rec.deliveries)
	}
}
