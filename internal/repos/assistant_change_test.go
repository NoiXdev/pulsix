package repos_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/repos"
)

type recordingWatcher struct {
	got []assistantchange.Change
	err error
}

func (w *recordingWatcher) Changed(_ context.Context, _ uuid.UUID, c assistantchange.Change) error {
	if w.err != nil {
		return w.err
	}
	w.got = append(w.got, c)
	return nil
}

// TestAnAssistantsRepoChangeIsHandedOn (spec 2026-11-25 C2): an update made
// with an assistant token that moves what a sync fetches or how its files
// are read (URL, branch, subpath, credential, vault identities) hands the
// watcher one change naming the fields that moved, the URL as a read
// shows it (RedactURL: a legacy URL never shows its secret). A rename, and a person's change, hand on nothing.
func TestAnAssistantsRepoChangeIsHandedOn(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	git := credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "deploy-key", Kind: credentials.KindGitKey}
	vault := credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "vault-prod", Kind: credentials.KindVaultPassword}
	token := uuid.Must(uuid.NewV7())
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), TokenID: &token, Assistant: true}
	for _, tc := range []struct {
		name   string
		mutate func(in *repos.UpdateInput)
		want   []assistantchange.Field
	}{
		{"rename", func(in *repos.UpdateInput) { in.Name = "infra-2" }, nil},
		{"url", func(in *repos.UpdateInput) { in.URL = "https://git.example.com/acme/other.git" },
			[]assistantchange.Field{{Name: "url", Old: "https://git.example.com/acme/infra.git", New: "https://git.example.com/acme/other.git"}}},
		{"branch", func(in *repos.UpdateInput) { in.Branch = "release" },
			[]assistantchange.Field{{Name: "branch", Old: "main", New: "release"}}},
		{"subpath", func(in *repos.UpdateInput) { in.Subpath = "ansible" },
			[]assistantchange.Field{{Name: "subpath", Old: nil, New: "ansible"}}},
		{"credential", func(in *repos.UpdateInput) { in.CredentialID = &git.ID },
			[]assistantchange.Field{{Name: "credential", Old: nil, New: "deploy-key"}}},
		{"vault identities", func(in *repos.UpdateInput) {
			in.VaultIdentities = []repos.VaultIdentity{{Label: "prod", CredentialID: vault.ID}}
		}, []assistantchange.Field{{Name: "vault_identities", Old: []string{}, New: []string{"prod: vault-prod"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, actor := range []assistantchange.Actor{assistant, {UserID: assistant.UserID}} {
				svc := newService(newFakeStore(), newFakeCreds(map[uuid.UUID]credentials.Credential{git.ID: git, vault.ID: vault}), &fakeEnqueuer{})
				w := &recordingWatcher{}
				svc.SetChangeWatcher(w)
				created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
				if err != nil {
					t.Fatal(err)
				}
				in := repos.UpdateInput{Name: created.Name, URL: created.URL, Branch: created.Branch, Subpath: created.Subpath, Actor: actor}
				tc.mutate(&in)
				if _, err := svc.Update(ctx, org, created.ID, in); err != nil {
					t.Fatal(err)
				}
				if !actor.Assistant || tc.want == nil {
					if len(w.got) != 0 {
						t.Fatalf("handed on: %+v", w.got)
					}
					continue
				}
				if len(w.got) != 1 {
					t.Fatalf("changes = %+v", w.got)
				}
				c := w.got[0]
				if c.Subject != assistantchange.SubjectRepo || c.ID != created.ID || c.TokenID == nil || *c.TokenID != token ||
					!reflect.DeepEqual(c.Fields, tc.want) {
					t.Fatalf("change = %#v", c)
				}
			}
		})
	}
}

// The watcher runs inside the update's transaction: its error fails it.
func TestARepoWatcherErrorFailsTheUpdate(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	svc := newService(newFakeStore(), newFakeCreds(nil), &fakeEnqueuer{})
	boom := errors.New("boom")
	svc.SetChangeWatcher(&recordingWatcher{err: boom})
	created, err := svc.Create(ctx, org, repos.CreateInput{Name: "infra", URL: "https://git.example.com/acme/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, org, created.ID, repos.UpdateInput{
		Name: "infra", URL: created.URL, Branch: "release", Actor: assistantchange.Actor{Assistant: true},
	}); !errors.Is(err, boom) {
		t.Fatalf("update = %v, want the watcher's error", err)
	}
}
