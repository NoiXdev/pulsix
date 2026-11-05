package repos_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// The variable source methods of the general fakeStore: it keeps none.
// sourcesStore below is the fake the variable source tests use.

func (f *fakeStore) ReplaceVarSources(context.Context, uuid.UUID, uuid.UUID, repos.SourcesRecord, []varsource.Playbook) error {
	return errors.New("fakeStore keeps no variable sources")
}

func (f *fakeStore) MarkVarSourcesUnread(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return errors.New("fakeStore keeps no variable sources")
}

func (f *fakeStore) GetVarSources(context.Context, uuid.UUID, uuid.UUID) (repos.SourcesRecord, error) {
	return repos.SourcesRecord{}, repos.ErrNotFound
}

func (f *fakeStore) ListPlaybookSources(context.Context, uuid.UUID, uuid.UUID) ([]varsource.Playbook, error) {
	return nil, nil
}

func (f *fakeStore) GetPlaybookSource(context.Context, uuid.UUID, uuid.UUID, string) (varsource.Playbook, error) {
	return varsource.Playbook{}, repos.ErrNotFound
}

// sourcesStore is one repository's variable sources in memory.
type sourcesStore struct {
	*fakeStore
	record     *repos.SourcesRecord
	playbooks  []varsource.Playbook
	replaced   int
	marked     []time.Time
	replaceErr error
}

func (s *sourcesStore) ReplaceVarSources(_ context.Context, _, _ uuid.UUID, rec repos.SourcesRecord, pbs []varsource.Playbook) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replaced++
	s.record = &rec
	s.playbooks = append([]varsource.Playbook(nil), pbs...)
	return nil
}

func (s *sourcesStore) MarkVarSourcesUnread(_ context.Context, _, _ uuid.UUID, at time.Time) error {
	s.marked = append(s.marked, at)
	return nil
}

func (s *sourcesStore) GetVarSources(context.Context, uuid.UUID, uuid.UUID) (repos.SourcesRecord, error) {
	if s.record == nil {
		return repos.SourcesRecord{}, repos.ErrNotFound
	}
	return *s.record, nil
}

func (s *sourcesStore) ListPlaybookSources(context.Context, uuid.UUID, uuid.UUID) ([]varsource.Playbook, error) {
	return append([]varsource.Playbook(nil), s.playbooks...), nil
}

// previousRead is a store holding a read of a.yml, b.yml and c.yml at
// commitA.
func previousRead() *sourcesStore {
	t0 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	return &sourcesStore{
		fakeStore: newFakeStore(),
		record:    &repos.SourcesRecord{Commit: commitA, SyncedAt: t0, WorkerReads: true, Notes: []varsource.Note{}},
		playbooks: []varsource.Playbook{
			playbookSource("a.yml", "old_a"), playbookSource("b.yml", "old_b"), playbookSource("c.yml", "old_c"),
		},
	}
}

func sourcesService(store *sourcesStore, log *bytes.Buffer) *repos.Service {
	svc := repos.NewService(store, nil, nil)
	svc.SetLogger(slog.New(slog.NewTextHandler(log, nil)))
	return svc
}

func TestRecordVarSources(t *testing.T) {
	org, repo := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	t.Run("an older worker sent no sources: the record is flagged", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, nil); err != nil {
			t.Fatal(err)
		}
		if len(store.marked) != 1 || !store.marked[0].Equal(at) || store.replaced != 0 {
			t.Fatalf("marked %v, replaced %d; want one mark at %v and no replace", store.marked, store.replaced, at)
		}
	})

	t.Run("a read replaces the record", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{
			playbookSource("a.yml", "new_a"), playbookSource("d.yml", "new_d"),
		}, Notes: []varsource.Note{varsource.ReadFailed("x")}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		rec := store.record
		if store.replaced != 1 || rec.Commit != commitB || !rec.SyncedAt.Equal(at) || !rec.WorkerReads || rec.Incomplete || len(rec.Notes) != 1 {
			t.Fatalf("record = %+v (replaced %d)", rec, store.replaced)
		}
		if !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", "d.yml"}) || fieldNames(store.playbooks[0])[0] != "new_a" {
			t.Fatalf("playbooks = %+v", store.playbooks)
		}
		if len(store.marked) != 0 || log.Len() != 0 {
			t.Fatalf("marked %v, log %q", store.marked, log.String())
		}
	})

	t.Run("a failed read keeps the commit and the playbooks", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
			Notes: []varsource.Note{varsource.ReadFailed("reading the variable sources failed; no playbooks read")}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		rec := store.record
		if rec.Commit != commitA || !rec.SyncedAt.Equal(at) || !rec.WorkerReads || len(rec.Notes) != 1 ||
			!strings.Contains(rec.Notes[0].Message, "no playbooks read") {
			t.Fatalf("record = %+v", rec)
		}
		if !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", "b.yml", "c.yml"}) || fieldNames(store.playbooks[0])[0] != "old_a" {
			t.Fatalf("playbooks = %+v, want the previous three", store.playbooks)
		}
	})

	t.Run("a failed first read stores an empty record", func(t *testing.T) {
		store, log := &sourcesStore{fakeStore: newFakeStore()}, &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
			Notes: []varsource.Note{varsource.ReadFailed("no playbooks read")}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		if rec := store.record; rec == nil || rec.Commit != "" || !rec.WorkerReads || len(rec.Notes) != 1 || len(store.playbooks) != 0 {
			t.Fatalf("record = %+v, playbooks %+v", rec, store.playbooks)
		}
	})

	t.Run("left-out playbooks keep their previous rows", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Commit: commitB, Incomplete: true,
			Playbooks: []varsource.Playbook{playbookSource("a.yml", "new_a")},
			LeftOut:   []string{"b.yml", "z.yml"}, Notes: []varsource.Note{}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		rec := store.record
		if rec.Commit != commitB || !rec.Incomplete || !sameStrings(rec.LeftOut, []string{"b.yml", "z.yml"}) {
			t.Fatalf("record = %+v", rec)
		}
		// a.yml read anew, b.yml left out and kept, c.yml no longer
		// discovered, z.yml left out but never read: no row.
		if !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", "b.yml"}) ||
			fieldNames(store.playbooks[0])[0] != "new_a" || fieldNames(store.playbooks[1])[0] != "old_b" {
			t.Fatalf("playbooks = %+v", store.playbooks)
		}
	})

	t.Run("an unread commit is not a new revision", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Commit: "", Playbooks: []varsource.Playbook{playbookSource("a.yml", "new_a")},
			Notes: []varsource.Note{varsource.ReadFailed("the commit of the repository could not be determined")}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		if store.record.Commit != commitA || !sameStrings(playbookPaths(store.playbooks), []string{"a.yml"}) {
			t.Fatalf("record = %+v, playbooks %v", store.record, playbookPaths(store.playbooks))
		}
	})

	t.Run("an unread commit without a previous record stays empty", func(t *testing.T) {
		store, log := &sourcesStore{fakeStore: newFakeStore()}, &bytes.Buffer{}
		raw := encodeReport(t, varsource.Report{Playbooks: []varsource.Playbook{playbookSource("a.yml")}, Notes: []varsource.Note{}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		if store.record.Commit != "" || len(store.playbooks) != 1 {
			t.Fatalf("record = %+v", store.record)
		}
	})

	t.Run("big integers survive the store", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := json.RawMessage(`{"commit":"` + commitB + `","playbooks":[{"path":"a.yml","failed":false,` +
			`"fields":[{"name":"big","type":"int","default":12345678901234567890}],"fields_cut":false,` +
			`"set_in_playbook":[],"roles":[],"files":[],"notes":[]}],"incomplete":false,"notes":[]}`)
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(store.playbooks[0].Fields[0].Default)
		if err != nil || string(data) != "12345678901234567890" {
			t.Fatalf("default = %s, %v", data, err)
		}
	})

	oversized := make([]byte, varsource.MaxReportBytes+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	copy(oversized, `{"commit":"`+commitB+`","playbooks":[],"notes":[]}`)
	malformed := map[string]json.RawMessage{
		"not JSON":         json.RawMessage(`{"commit":`),
		"null":             json.RawMessage(`null`),
		"not an object":    json.RawMessage(`[1,2]`),
		"wrong shape":      json.RawMessage(`{"commit":7}`),
		"oversized":        oversized,
		"an empty path":    encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{playbookSource("")}}),
		"a repeated path":  encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{playbookSource("a.yml"), playbookSource("a.yml")}}),
		"a NUL character":  json.RawMessage(`{"commit":"` + commitB + `","playbooks":[],"notes":[{"level":"warning","code":"x","message":"a\u0000b"}]}`),
		"too many entries": encodeReport(t, varsource.Report{Commit: commitB, Playbooks: manyPlaybooks(varsource.MaxPlaybooks + 1)}),
	}
	for name, raw := range malformed {
		t.Run("malformed: "+name, func(t *testing.T) {
			store, log := previousRead(), &bytes.Buffer{}
			before := *store.record
			if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
				t.Fatalf("RecordVarSources = %v, want nil (a malformed report never fails the sync)", err)
			}
			if store.replaced != 0 || len(store.marked) != 0 || store.record.Commit != before.Commit || len(store.playbooks) != 3 {
				t.Fatalf("store touched: replaced %d, marked %v, record %+v", store.replaced, store.marked, store.record)
			}
			if lines := strings.Count(strings.TrimSpace(log.String()), "\n") + 1; log.Len() == 0 || lines != 1 {
				t.Fatalf("log = %q, want one line", log.String())
			}
		})
	}

	t.Run("an escaped backslash before u0000 is no NUL", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		raw := json.RawMessage(`{"commit":"` + commitB + `","playbooks":[],"notes":[{"level":"warning","code":"x","message":"a\\u0000b"}]}`)
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatal(err)
		}
		if store.replaced != 1 || store.record.Notes[0].Message != `a\u0000b` {
			t.Fatalf("record = %+v (replaced %d), log %q", store.record, store.replaced, log.String())
		}
	})

	t.Run("a repository gone meanwhile is skipped", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		store.replaceErr = repos.ErrNotFound
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err != nil {
			t.Fatalf("RecordVarSources = %v, want nil", err)
		}
		if log.Len() == 0 {
			t.Fatal("a skipped record must be logged")
		}
	})

	t.Run("a store failure is returned", func(t *testing.T) {
		store, log := previousRead(), &bytes.Buffer{}
		store.replaceErr = errors.New("connection reset")
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
		if err := sourcesService(store, log).RecordVarSources(ctx, org, repo, at, raw); err == nil {
			t.Fatal("RecordVarSources = nil, want the store failure (the finish retries)")
		}
	})
}

func manyPlaybooks(n int) []varsource.Playbook {
	out := make([]varsource.Playbook, n)
	for i := range out {
		out[i] = varsource.Playbook{Path: uuid.NewString() + ".yml"}
	}
	return out
}
