package triggers_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/triggers"
)

// Validate checks a trigger as Create and Update would and writes nothing:
// it returns the trigger as it would be stored (defaults filled in), refuses
// a name another trigger has, and for an existing trigger keeps its kind
// and target.
func TestValidateChecksWithoutWriting(t *testing.T) {
	w := newWorld(t)
	id := uuid.Must(uuid.NewV7())
	got, err := w.svc.Validate(ctx, w.org, id, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.OnActive != triggers.OnActiveQueue || got.RateLimit != triggers.DefaultRateLimit() || got.Name != "deploy on push" {
		t.Fatalf("validated = %+v", got)
	}
	if len(w.store.byID) != 0 {
		t.Fatal("Validate wrote a trigger")
	}
	existing, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Validate(ctx, w.org, id, w.templateInput()); !errors.Is(err, triggers.ErrNameTaken) {
		t.Fatalf("a taken name: %v", err)
	}
	// The trigger itself may keep its name.
	if _, err := w.svc.Validate(ctx, w.org, existing.Trigger.ID, w.templateInput()); err != nil {
		t.Fatalf("own name: %v", err)
	}
	moved := w.templateInput()
	moved.Kind, moved.TemplateID, moved.RepoID = triggers.KindRepoSync, nil, &w.repo
	if _, err := w.svc.Validate(ctx, w.org, existing.Trigger.ID, moved); !errors.Is(err, triggers.ErrInvalidInput) {
		t.Fatalf("a changed kind: %v", err)
	}
	bad := w.templateInput()
	bad.Mapping.ExtraVars = map[string]string{"ansible_host": "$.x"}
	if _, err := w.svc.Validate(ctx, w.org, id, bad); !errors.Is(err, triggers.ErrInvalidInput) {
		t.Fatalf("a reserved mapping target: %v", err)
	}
}

// CreateAs stores the trigger under the id given, attributed to the actor,
// and returns its secret once.
func TestCreateAsTakesTheGivenID(t *testing.T) {
	w := newWorld(t)
	id := uuid.Must(uuid.NewV7())
	c, err := w.svc.CreateAs(ctx, w.org, id, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if c.Trigger.ID != id || c.Trigger.CreatedBy == nil || *c.Trigger.CreatedBy != w.actor || len(c.Secret) != 43 || c.URL != "https://pulsix.example.test/api/v1/hooks/jobs/"+id.String() {
		t.Fatalf("created = %+v", c)
	}
	if string(w.store.secrets[id]) != "sealed:"+string(triggers.AAD(w.org, id))+":"+c.Secret {
		t.Fatalf("sealed = %q", w.store.secrets[id])
	}
}

// RotateSecretAt replaces the secret of exactly the revision given.
func TestRotateSecretAtTheRevisionSeen(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RotateSecretAt(ctx, w.org, c.Trigger.ID, c.Trigger.Revision+1); !errors.Is(err, triggers.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	r, err := w.svc.RotateSecretAt(ctx, w.org, c.Trigger.ID, c.Trigger.Revision)
	if err != nil || r.Secret == "" || r.Secret == c.Secret {
		t.Fatalf("rotated = %+v %v", r, err)
	}
	if string(w.store.secrets[c.Trigger.ID]) != "sealed:"+string(triggers.AAD(w.org, c.Trigger.ID))+":"+r.Secret {
		t.Fatal("the new secret is not the stored one")
	}
}

// Disable turns a trigger off.
func TestDisable(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	off, err := w.svc.Disable(ctx, w.org, c.Trigger.ID)
	if err != nil || off.Enabled {
		t.Fatalf("Disable = %+v %v", off, err)
	}
	if _, err := w.svc.Disable(ctx, w.org, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("unknown trigger: %v", err)
	}
}
