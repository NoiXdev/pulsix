package repos_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
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

func (f *fakeStore) LockVarSources(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("fakeStore keeps no variable sources")
}

func (f *fakeStore) GetVarSources(context.Context, uuid.UUID, uuid.UUID) (repos.SourcesRecord, error) {
	return repos.SourcesRecord{}, repos.ErrNotFound
}

func (f *fakeStore) ListPlaybookSources(context.Context, uuid.UUID, uuid.UUID) ([]varsource.Playbook, error) {
	return nil, nil
}

func (f *fakeStore) GetPlaybookVarSource(context.Context, uuid.UUID, uuid.UUID, string) (repos.SourcesRecord, *varsource.Playbook, error) {
	return repos.SourcesRecord{}, nil, repos.ErrNotFound
}

// sourcesStore is one repository's variable sources in memory; ops is
// the order of its calls.
type sourcesStore struct {
	*fakeStore
	record     *repos.SourcesRecord
	playbooks  []varsource.Playbook
	replaced   int
	marked     []time.Time
	ops        []string
	lockErr    error
	replaceErr error
}

func (s *sourcesStore) LockVarSources(context.Context, uuid.UUID, uuid.UUID) error {
	s.ops = append(s.ops, "lock")
	return s.lockErr
}

func (s *sourcesStore) ReplaceVarSources(_ context.Context, _, _ uuid.UUID, rec repos.SourcesRecord, pbs []varsource.Playbook) error {
	s.ops = append(s.ops, "replace")
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
	s.ops = append(s.ops, "get")
	if s.record == nil {
		return repos.SourcesRecord{}, repos.ErrNotFound
	}
	return *s.record, nil
}

func (s *sourcesStore) ListPlaybookSources(context.Context, uuid.UUID, uuid.UUID) ([]varsource.Playbook, error) {
	s.ops = append(s.ops, "list")
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

// record runs RecordVarSources against store and returns what it
// reported and logged.
func record(t *testing.T, store *sourcesStore, at time.Time, raw json.RawMessage) (stored bool, log string) {
	t.Helper()
	var buf bytes.Buffer
	stored, err := sourcesService(store, &buf).RecordVarSources(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), at, raw)
	if err != nil {
		t.Fatalf("RecordVarSources = %v, want nil", err)
	}
	return stored, buf.String()
}

func logLines(log string) int {
	if log == "" {
		return 0
	}
	return strings.Count(strings.TrimSpace(log), "\n") + 1
}

func TestRecordVarSources(t *testing.T) {
	at := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	t.Run("an older worker sent no sources: the record is flagged", func(t *testing.T) {
		store := previousRead()
		if stored, _ := record(t, store, at, nil); stored {
			t.Fatal("stored = true, want false (nothing of a worker's read was stored)")
		}
		if len(store.marked) != 1 || !store.marked[0].Equal(at) || store.replaced != 0 {
			t.Fatalf("marked %v, replaced %d; want one mark at %v and no replace", store.marked, store.replaced, at)
		}
	})

	t.Run("a read replaces the record", func(t *testing.T) {
		store := previousRead()
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{
			playbookSource("a.yml", "new_a"), playbookSource("d.yml", "new_d"),
		}, Notes: []varsource.Note{varsource.ReadFailed("x")}})
		stored, log := record(t, store, at, raw)
		rec := store.record
		if !stored || store.replaced != 1 || rec.Commit != commitB || !rec.SyncedAt.Equal(at) || !rec.WorkerReads || rec.Incomplete || len(rec.Notes) != 1 {
			t.Fatalf("stored %v, record = %+v (replaced %d)", stored, rec, store.replaced)
		}
		if !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", "d.yml"}) || fieldNames(store.playbooks[0])[0] != "new_a" {
			t.Fatalf("playbooks = %+v", store.playbooks)
		}
		if len(store.marked) != 0 || log != "" {
			t.Fatalf("marked %v, log %q", store.marked, log)
		}
	})

	t.Run("the repository is locked before the previous record is read", func(t *testing.T) {
		store := previousRead()
		raw := encodeReport(t, varsource.Report{Commit: "", Incomplete: true, LeftOut: []string{"b.yml"},
			Playbooks: []varsource.Playbook{playbookSource("a.yml")}, Notes: []varsource.Note{}})
		record(t, store, at, raw)
		if want := []string{"lock", "get", "list", "replace"}; !sameStrings(store.ops, want) {
			t.Fatalf("store calls = %v, want %v", store.ops, want)
		}
	})

	t.Run("a failed read keeps the commit and the playbooks", func(t *testing.T) {
		store := previousRead()
		raw := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
			Notes: []varsource.Note{varsource.ReadFailed("reading the variable sources failed; no playbooks read")}})
		stored, _ := record(t, store, at, raw)
		rec := store.record
		if stored || rec.Commit != commitA || !rec.SyncedAt.Equal(at) || !rec.WorkerReads || len(rec.Notes) != 1 ||
			!strings.Contains(rec.Notes[0].Message, "no playbooks read") {
			t.Fatalf("stored %v (want false: no new sources), record = %+v", stored, rec)
		}
		if !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", "b.yml", "c.yml"}) || fieldNames(store.playbooks[0])[0] != "old_a" {
			t.Fatalf("playbooks = %+v, want the previous three", store.playbooks)
		}
	})

	t.Run("a failed first read stores an empty record", func(t *testing.T) {
		store := &sourcesStore{fakeStore: newFakeStore()}
		raw := encodeReport(t, varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
			Notes: []varsource.Note{varsource.ReadFailed("no playbooks read")}})
		record(t, store, at, raw)
		if rec := store.record; rec == nil || rec.Commit != "" || !rec.WorkerReads || len(rec.Notes) != 1 || len(store.playbooks) != 0 {
			t.Fatalf("record = %+v, playbooks %+v", rec, store.playbooks)
		}
	})

	t.Run("left-out playbooks keep their previous rows", func(t *testing.T) {
		store := previousRead()
		raw := encodeReport(t, varsource.Report{Commit: commitB, Incomplete: true,
			Playbooks: []varsource.Playbook{playbookSource("a.yml", "new_a")},
			LeftOut:   []string{"b.yml", "z.yml"}, Notes: []varsource.Note{}})
		if stored, _ := record(t, store, at, raw); !stored {
			t.Fatal("stored = false, want true")
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
		store := previousRead()
		raw := encodeReport(t, varsource.Report{Commit: "", Playbooks: []varsource.Playbook{playbookSource("a.yml", "new_a")},
			Notes: []varsource.Note{varsource.ReadFailed("the commit of the repository could not be determined")}})
		record(t, store, at, raw)
		if store.record.Commit != commitA || !sameStrings(playbookPaths(store.playbooks), []string{"a.yml"}) {
			t.Fatalf("record = %+v, playbooks %v", store.record, playbookPaths(store.playbooks))
		}
	})

	t.Run("an unread commit without a previous record stays empty", func(t *testing.T) {
		store := &sourcesStore{fakeStore: newFakeStore()}
		raw := encodeReport(t, varsource.Report{Playbooks: []varsource.Playbook{playbookSource("a.yml")}, Notes: []varsource.Note{}})
		record(t, store, at, raw)
		if store.record.Commit != "" || len(store.playbooks) != 1 {
			t.Fatalf("record = %+v", store.record)
		}
	})

	t.Run("a playbook path too long to store is dropped, the rest stored", func(t *testing.T) {
		store := previousRead()
		long := strings.Repeat("p", repos.MaxStoredPathBytes) + ".yml"
		atLimit := strings.Repeat("q", repos.MaxStoredPathBytes-4) + ".yml"
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{
			playbookSource("a.yml"), playbookSource(long), playbookSource(atLimit),
		}, Notes: []varsource.Note{}})
		stored, log := record(t, store, at, raw)
		if !stored || !sameStrings(playbookPaths(store.playbooks), []string{"a.yml", atLimit}) {
			t.Fatalf("stored %v, playbooks = %v", stored, playbookPaths(store.playbooks))
		}
		notes := store.record.Notes
		if len(notes) != 1 || notes[0].Code != "path_too_long" || len(notes[0].File) > 300 {
			t.Fatalf("notes = %+v, want one path_too_long with the path clipped", notes)
		}
		if logLines(log) != 1 {
			t.Fatalf("log = %q, want one line", log)
		}
	})

	oversized := make([]byte, varsource.MaxReportBytes+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	copy(oversized, `{"commit":"`+commitB+`","playbooks":[],"notes":[]}`)
	deep := `{"commit":"` + commitB + `","playbooks":[],"notes":[],"x":` + strings.Repeat("[", 200) + strings.Repeat("]", 200) + `}`
	malformed := map[string]json.RawMessage{
		"not JSON":         json.RawMessage(`{"commit":`),
		"trailing data":    json.RawMessage(`{"commit":"x","playbooks":[],"notes":[]} {}`),
		"null":             json.RawMessage(`null`),
		"not an object":    json.RawMessage(`[1,2]`),
		"wrong shape":      json.RawMessage(`{"commit":7}`),
		"oversized":        oversized,
		"nested too deep":  json.RawMessage(deep),
		"an empty path":    encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{playbookSource("")}}),
		"a repeated path":  encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{playbookSource("a.yml"), playbookSource("a.yml")}}),
		"a NUL character":  json.RawMessage(`{"commit":"` + commitB + `","playbooks":[],"notes":[{"level":"warning","code":"x","message":"a\u0000b"}]}`),
		"a NUL in a key":   json.RawMessage(`{"commit":"` + commitB + `","playbooks":[],"notes":[],"a\u0000":1}`),
		"too many entries": encodeReport(t, varsource.Report{Commit: commitB, Playbooks: manyPlaybooks(varsource.MaxPlaybooks + 1)}),
	}
	for name, raw := range malformed {
		t.Run("malformed: "+name, func(t *testing.T) {
			store := previousRead()
			before := *store.record
			stored, log := record(t, store, at, raw)
			if stored || store.replaced != 0 || len(store.marked) != 0 || store.record.Commit != before.Commit || len(store.playbooks) != 3 {
				t.Fatalf("store touched: stored %v, replaced %d, marked %v, record %+v", stored, store.replaced, store.marked, store.record)
			}
			if logLines(log) != 1 {
				t.Fatalf("log = %q, want one line", log)
			}
		})
	}

	t.Run("an escaped backslash before u0000 is no NUL", func(t *testing.T) {
		store := previousRead()
		raw := json.RawMessage(`{"commit":"` + commitB + `","playbooks":[],"notes":[{"level":"warning","code":"x","message":"a\\u0000b \"[{"}]}`)
		stored, log := record(t, store, at, raw)
		if !stored || store.record.Notes[0].Message != `a\u0000b "[{` {
			t.Fatalf("record = %+v (stored %v), log %q", store.record, stored, log)
		}
	})

	t.Run("a repository gone meanwhile is skipped before anything is read", func(t *testing.T) {
		store := previousRead()
		store.lockErr = repos.ErrNotFound
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
		stored, log := record(t, store, at, raw)
		if stored || !sameStrings(store.ops, []string{"lock"}) || logLines(log) != 1 {
			t.Fatalf("stored %v, store calls %v, log %q; want only the lock and one log line", stored, store.ops, log)
		}
	})

	t.Run("a repository gone at the write is skipped", func(t *testing.T) {
		store := previousRead()
		store.replaceErr = repos.ErrNotFound
		raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
		if stored, log := record(t, store, at, raw); stored || logLines(log) != 1 {
			t.Fatalf("stored %v, log %q", stored, log)
		}
	})

	for name, store := range map[string]*sourcesStore{
		"lock":  {fakeStore: newFakeStore(), lockErr: errors.New("connection reset")},
		"write": {fakeStore: newFakeStore(), replaceErr: errors.New("connection reset")},
	} {
		t.Run("a store failure at the "+name+" is returned", func(t *testing.T) {
			raw := encodeReport(t, varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}})
			if _, err := sourcesService(store, &bytes.Buffer{}).RecordVarSources(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), at, raw); err == nil {
				t.Fatal("RecordVarSources = nil, want the store failure (the finish retries)")
			}
		})
	}
}

func manyPlaybooks(n int) []varsource.Playbook {
	out := make([]varsource.Playbook, n)
	for i := range out {
		out[i] = varsource.Playbook{Path: uuid.NewString() + ".yml"}
	}
	return out
}

func (s *sourcesStore) GetPlaybookVarSource(_ context.Context, _, _ uuid.UUID, path string) (repos.SourcesRecord, *varsource.Playbook, error) {
	s.ops = append(s.ops, "get playbook")
	if s.record == nil {
		return repos.SourcesRecord{}, nil, repos.ErrNotFound
	}
	for _, pb := range s.playbooks {
		if pb.Path == path {
			return *s.record, &pb, nil
		}
	}
	return *s.record, nil, nil
}

// TestPlaybookVarSourceReadsOnce: the record and the playbook's row come
// from one store read (backlog 7.87), whatever it finds.
func TestPlaybookVarSourceReadsOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		store    *sourcesStore
		path     string
		rec, row bool
	}{
		{"record and row", previousRead(), "b.yml", true, true},
		{"record without the row", previousRead(), "z.yml", true, false},
		{"no record", &sourcesStore{fakeStore: newFakeStore()}, "b.yml", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			rec, pb, err := sourcesService(tc.store, &log).PlaybookVarSource(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), tc.path)
			if err != nil || (rec != nil) != tc.rec || (pb != nil) != tc.row {
				t.Fatalf("PlaybookVarSource = %+v, %+v, %v; want record %v, row %v", rec, pb, err, tc.rec, tc.row)
			}
			if tc.row && pb.Path != tc.path {
				t.Fatalf("row %s, want %s", pb.Path, tc.path)
			}
			if !sameStrings(tc.store.ops, []string{"get playbook"}) {
				t.Fatalf("store calls = %v, want one GetPlaybookVarSource", tc.store.ops)
			}
		})
	}
}

// unplainReport is a minimal report whose one playbook declares a field
// with the given label and default, both JSON text.
func unplainReport(label, def string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"commit":%q,"playbooks":[{"path":"site.yml","failed":false,`+
		`"fields":[{"name":"x","label":%s,"type":"float","default":%s}],"fields_cut":false,`+
		`"set_in_playbook":[],"roles":[],"files":[],"notes":[]}],"incomplete":false,"notes":[]}`, commitB, label, def))
}

// TestDecodeReportRefusesAnUnplainNumber (backlog 7.122): a report holding
// a number Go would write in exponent form (1e21 or more, or below 1e-6,
// zero aside) is refused whole, whatever the worker's version: jsonb keeps
// such a number as numeric and writes it out in full. A number inside a
// string is text and counts for nothing.
func TestDecodeReportRefusesAnUnplainNumber(t *testing.T) {
	for _, tc := range []struct {
		name, label, def string
		refused          bool
	}{
		{"1e300", `"x"`, `1e300`, true},
		{"-1e300", `"x"`, `-1e300`, true},
		{"1e-7", `"x"`, `1e-7`, true},
		{"1e21 written out", `"x"`, `1` + strings.Repeat("0", 21), true},
		{"1e20 written out", `"x"`, `100000000000000000000`, false},
		{"1e-6 written out", `"x"`, `0.000001`, false},
		{"zero", `"x"`, `0`, false},
		{"in a string", `"x"`, `"1e300"`, false},
		{"after an escaped quote in a string", `"a\"1e300"`, `1`, false},
		{"after a string ending in an escaped backslash", `"a\\"`, `1e300`, true},
		{"in a list", `"x"`, `[1, 2, 1e300]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := unplainReport(tc.label, tc.def)
			if !json.Valid(raw) {
				t.Fatalf("test report is not valid JSON: %s", raw)
			}
			err := repos.DecodeReport(raw)
			switch {
			case tc.refused && (err == nil || !strings.Contains(err.Error(), "1e21 or more, or below 1e-6")):
				t.Fatalf("DecodeReport = %v, want the number range refusal", err)
			case !tc.refused && err != nil:
				t.Fatalf("DecodeReport = %v, want nil", err)
			}
		})
	}

	t.Run("RecordVarSources keeps the stored record", func(t *testing.T) {
		store := previousRead()
		stored, log := record(t, store, time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), unplainReport(`"x"`, `1e300`))
		if stored || store.replaced != 0 || len(store.marked) != 0 || store.record.Commit != commitA {
			t.Fatalf("stored %v, replaced %d, marked %v, record %+v; want the previous record untouched", stored, store.replaced, store.marked, store.record)
		}
		if !strings.Contains(log, "variable sources ignored") || logLines(log) != 1 {
			t.Fatalf("log = %q, want one 'variable sources ignored' line", log)
		}
	})
}

// TestDecodeReportAcceptsWhatTheCurrentWorkerSends (backlog 7.122): the
// number check refuses nothing a current worker sends. The playbook goes
// through the worker's parser and encoder; 1e300 is no form value there
// (marker_value_type), every other number is, and the report is accepted.
func TestDecodeReportAcceptsWhatTheCurrentWorkerSends(t *testing.T) {
	fsys := fstest.MapFS{"site.yml": &fstest.MapFile{Data: []byte(`- hosts: all
  vars:
    zero: 0  # pulsix:
    neg: -3  # pulsix:
    half: 0.5  # pulsix:
    tiny: 1e-6  # pulsix:
    big: 1e20  # pulsix:
    wide: 9007199254740993  # pulsix:
    huge: 1e300  # pulsix:
  tasks: []
`)}}
	r := varsource.ParseRepo(ctx, fsys, commitB)
	if len(r.Playbooks) != 1 {
		t.Fatalf("playbooks = %+v, want site.yml", r.Playbooks)
	}
	defaults := map[string]any{}
	for _, f := range r.Playbooks[0].Fields {
		defaults[f.Name] = f.Default
	}
	want := map[string]any{"zero": 0, "neg": -3, "half": 0.5, "tiny": 1e-6, "big": 1e20, "wide": 9007199254740993}
	if len(defaults) != len(want) {
		t.Fatalf("field defaults = %#v, want %#v", defaults, want)
	}
	for name, v := range want {
		if defaults[name] != v {
			t.Fatalf("default of %s = %#v, want %#v", name, defaults[name], v)
		}
	}
	refusedHuge := false
	for _, n := range r.Playbooks[0].Notes {
		refusedHuge = refusedHuge || (n.Code == "marker_value_type" && strings.Contains(n.Message, "huge"))
	}
	if !refusedHuge {
		t.Fatalf("notes = %+v, want huge refused as a form value", r.Playbooks[0].Notes)
	}

	data, _, err := varsource.Encode(r, varsource.MaxReportBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"default":100000000000000000000`, `"default":0.000001`, `"default":9007199254740993`} {
		if !strings.Contains(string(data), s) {
			t.Fatalf("encoded report lacks %s:\n%s", s, data)
		}
	}
	if err := repos.DecodeReport(data); err != nil {
		t.Fatalf("DecodeReport(current worker's report) = %v, want nil", err)
	}
}
