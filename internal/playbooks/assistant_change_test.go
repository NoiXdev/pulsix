package playbooks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/playbooks"
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

// TestAnAssistantsPlaybookEditIsHandedOn (spec 2026-11-25 C2, fix round 1):
// an assistant's change of an editor playbook's text changes what every
// template that runs it runs, so it is handed on with the old and new
// text; the same text, and a person's edit, hand on nothing; the
// watcher's error fails the save.
func TestAnAssistantsPlaybookEditIsHandedOn(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	svc := newService(newFakeStore(), newFakeRepos(nil))
	w := &recordingWatcher{}
	svc.SetChangeWatcher(w)
	editor, err := svc.CreateEditor(ctx, org, playbooks.CreateInput{Inline: validPlaybook})
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.Must(uuid.NewV7())
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), TokenID: &token, Assistant: true}
	next := "- name: deploy\n  hosts: web\n  tasks: []\n"
	if _, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Inline: next, Actor: assistantchange.Actor{UserID: assistant.UserID}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 0 {
		t.Fatalf("a person's edit was handed on: %+v", w.got)
	}
	third := "- name: deploy\n  hosts: db\n  tasks: []\n"
	if _, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Inline: third, Actor: assistant}, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("changes = %+v", w.got)
	}
	c := w.got[0]
	if c.Subject != assistantchange.SubjectPlaybook || c.ID != editor.ID || c.Name != "deploy" || c.TokenID == nil || *c.TokenID != token ||
		len(c.Fields) != 1 || c.Fields[0].Name != "playbook_text" || c.Fields[0].Old != next || c.Fields[0].New != third {
		t.Fatalf("change = %#v", c)
	}
	if _, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Inline: third, Actor: assistant}, nil); err != nil || len(w.got) != 1 {
		t.Fatalf("same text = %v, changes %d", err, len(w.got))
	}
	boom := errors.New("boom")
	w.err = boom
	if _, err := svc.UpdateEditor(ctx, org, editor.ID, playbooks.CreateInput{Inline: validPlaybook, Actor: assistant}, nil); !errors.Is(err, boom) {
		t.Fatalf("watcher error = %v", err)
	}
}
