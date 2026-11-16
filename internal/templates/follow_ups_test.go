package templates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/templates"
)

// --- fake store: follow-ups ---

func (f *fakeStore) LockFollowUpGraph(context.Context, uuid.UUID) error { return nil }

func (f *fakeStore) ReplaceFollowUps(_ context.Context, _, templateID uuid.UUID, ids []uuid.UUID) error {
	f.follow[templateID] = append([]uuid.UUID(nil), ids...)
	return nil
}

func (f *fakeStore) followUps(templateID uuid.UUID) []templates.FollowUp {
	var out []templates.FollowUp
	for _, id := range f.follow[templateID] {
		out = append(out, templates.FollowUp{TemplateID: id, Name: f.byID[id].Name})
	}
	return out
}

func (f *fakeStore) ListFollowUps(_ context.Context, _, templateID uuid.UUID) ([]templates.FollowUp, error) {
	return f.followUps(templateID), nil
}

func (f *fakeStore) ListOrgFollowUps(_ context.Context, _ uuid.UUID) (map[uuid.UUID][]templates.FollowUp, error) {
	out := map[uuid.UUID][]templates.FollowUp{}
	for id := range f.follow {
		if fu := f.followUps(id); len(fu) > 0 {
			out[id] = fu
		}
	}
	return out, nil
}

func (f *fakeStore) FollowUpReferrers(_ context.Context, _, templateID uuid.UUID) ([]string, error) {
	var names []string
	for id, list := range f.follow {
		for _, to := range list {
			if to == templateID {
				names = append(names, f.byID[id].Name)
			}
		}
	}
	return names, nil
}

// seed stores a bare template named name.
func (f fixture) seed(name string) templates.Template {
	t := templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Name: name, PlaybookID: f.playbook}
	f.store.byID[t.ID] = t
	return t
}

func (f fixture) updateIn(t templates.Template, followUps *[]uuid.UUID) templates.UpdateInput {
	return templates.UpdateInput{Name: t.Name, PlaybookID: t.PlaybookID, FollowUps: followUps}
}

func TestUpdateRefusesCycle(t *testing.T) {
	f := newFixture()
	a, b := f.seed("a"), f.seed("b")
	f.store.follow[b.ID] = []uuid.UUID{a.ID}

	_, err := f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, &[]uuid.UUID{b.ID}))
	if !errors.Is(err, templates.ErrFollowUpCycle) || !errors.Is(err, templates.ErrInvalidInput) {
		t.Fatalf("Update error = %v, want ErrFollowUpCycle and ErrInvalidInput", err)
	}
	if want := "follow-ups would form a cycle: a → b → a"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if len(f.store.follow[a.ID]) != 0 {
		t.Fatalf("a refused save stored %v", f.store.follow[a.ID])
	}
}

func TestUpdateRefusesSelfDuplicateAndEleven(t *testing.T) {
	f := newFixture()
	a, b := f.seed("a"), f.seed("b")
	var eleven []uuid.UUID
	for range 11 {
		eleven = append(eleven, f.seed("t-"+uuid.NewString()[:8]).ID)
	}
	for name, ids := range map[string][]uuid.UUID{
		"self": {a.ID}, "duplicate": {b.ID, b.ID}, "eleven": eleven,
	} {
		_, err := f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, &ids))
		if !errors.Is(err, templates.ErrInvalidInput) {
			t.Fatalf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
	ten := eleven[:10]
	if _, err := f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, &ten)); err != nil {
		t.Fatalf("ten follow-ups: %v", err)
	}
}

func TestUpdateRefusesUnknownFollowUp(t *testing.T) {
	f := newFixture()
	a := f.seed("a")
	_, err := f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, &[]uuid.UUID{uuid.New()}))
	if !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestUpdateNilFollowUpsKeepsList(t *testing.T) {
	f := newFixture()
	a, b := f.seed("a"), f.seed("b")
	f.store.follow[a.ID] = []uuid.UUID{b.ID}

	got, err := f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FollowUps) != 1 || got.FollowUps[0].TemplateID != b.ID || got.FollowUps[0].Name != "b" {
		t.Fatalf("FollowUps = %+v, want [b]", got.FollowUps)
	}
	got, err = f.svc.Update(ctx, f.org, a.ID, f.updateIn(a, &[]uuid.UUID{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FollowUps) != 0 || len(f.store.follow[a.ID]) != 0 {
		t.Fatalf("an empty slice must clear: %+v", got.FollowUps)
	}
}

func TestCreateStoresFollowUpsAndListFillsThem(t *testing.T) {
	f := newFixture()
	b := f.seed("b")
	in := f.in()
	in.FollowUps = []uuid.UUID{b.ID}
	created, err := f.svc.Create(ctx, f.org, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.FollowUps) != 1 || created.FollowUps[0].Name != "b" {
		t.Fatalf("created.FollowUps = %+v", created.FollowUps)
	}
	list, err := f.svc.List(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range list {
		if tpl.ID == created.ID && (len(tpl.FollowUps) != 1 || tpl.FollowUps[0].TemplateID != b.ID) {
			t.Fatalf("List FollowUps = %+v", tpl.FollowUps)
		}
	}
	got, err := f.svc.Get(ctx, f.org, created.ID)
	if err != nil || len(got.FollowUps) != 1 {
		t.Fatalf("Get = %+v, %v", got.FollowUps, err)
	}
}

func TestDeleteReferencedTemplate(t *testing.T) {
	f := newFixture()
	cloudpanel, b := f.seed("cloudpanel"), f.seed("b")
	f.store.follow[cloudpanel.ID] = []uuid.UUID{b.ID}

	err := f.svc.Delete(ctx, f.org, b.ID)
	var inUse *templates.InUseError
	if !errors.As(err, &inUse) || len(inUse.Referrers) != 1 || inUse.Referrers[0] != "cloudpanel" {
		t.Fatalf("Delete error = %v, want *InUseError{cloudpanel}", err)
	}
	if want := "this template is a follow-up of: cloudpanel"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
	if _, ok := f.store.byID[b.ID]; !ok {
		t.Fatal("a refused delete removed the template")
	}
}
