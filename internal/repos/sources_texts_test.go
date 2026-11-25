// The playbook texts a sync sent beside its report (backlog 3.127): what
// RecordVarSources stores for each reported path, the limits it checks
// again, and the previous texts it keeps.
package repos_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

const playText = "- hosts: all\n  tasks: []\n"

// encodeTexts is the two texts keys as the job finish hands them over.
func encodeTexts(t *testing.T, texts, leftOut map[string]any) json.RawMessage {
	t.Helper()
	m := map[string]any{}
	if texts != nil {
		m[varsource.PlaybookTextsStatsKey] = texts
	}
	if leftOut != nil {
		m[varsource.PlaybookTextsLeftOutStatsKey] = leftOut
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// recordTexts runs RecordVarSources with the report and texts.
func recordTexts(t *testing.T, store *sourcesStore, report varsource.Report, texts json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := sourcesService(store, &buf).RecordVarSources(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
		time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), encodeReport(t, report), texts, ""); err != nil {
		t.Fatalf("RecordVarSources = %v, want nil", err)
	}
	return buf.String()
}

func reportOf(paths ...string) varsource.Report {
	r := varsource.Report{Commit: commitB, Notes: []varsource.Note{}}
	for _, p := range paths {
		r.Playbooks = append(r.Playbooks, playbookSource(p))
	}
	return r
}

func text(s string) repos.PlaybookText { return repos.PlaybookText{Content: &s} }

func leftOut(reason string) repos.PlaybookText { return repos.PlaybookText{LeftOut: reason} }

// assertTexts compares the stored texts with want, path by path.
func assertTexts(t *testing.T, got, want map[string]repos.PlaybookText) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("texts = %s, want %s", describeTexts(got), describeTexts(want))
	}
	for p, w := range want {
		g, ok := got[p]
		if !ok || g.LeftOut != w.LeftOut || (g.Content == nil) != (w.Content == nil) ||
			(g.Content != nil && *g.Content != *w.Content) {
			t.Fatalf("texts = %s, want %s", describeTexts(got), describeTexts(want))
		}
	}
}

// describeTexts names each path's text by its length (texts run to
// 512 KiB) or its reason.
func describeTexts(m map[string]repos.PlaybookText) string {
	var b strings.Builder
	for p, tx := range m {
		switch {
		case tx.Content != nil:
			fmt.Fprintf(&b, "%s: %d bytes; ", p, len(*tx.Content))
		case tx.LeftOut != "":
			fmt.Fprintf(&b, "%s: %s; ", p, tx.LeftOut)
		default:
			fmt.Fprintf(&b, "%s: none; ", p)
		}
	}
	return "{" + b.String() + "}"
}

func TestRecordVarSourcesStoresEachReportedPathsText(t *testing.T) {
	store := &sourcesStore{fakeStore: newFakeStore()}
	log := recordTexts(t, store, reportOf("a.yml", "b.yml", "c.yml", "d.yml", "e.yml"), encodeTexts(t,
		map[string]any{"a.yml": playText, "e.yml": "", "elsewhere.yml": playText},
		map[string]any{"b.yml": varsource.TextTooLarge, "c.yml": "a reason from the future", "gone.yml": varsource.TextBudget}))
	// d.yml is in neither map: no text and no reason (served as not sent).
	// A path the report does not name is ignored; a reason this manager
	// does not know is unreadable.
	assertTexts(t, store.texts, map[string]repos.PlaybookText{
		"a.yml": text(playText), "e.yml": text(""),
		"b.yml": leftOut(varsource.TextTooLarge), "c.yml": leftOut(varsource.TextUnreadable),
	})
	if log != "" {
		t.Fatalf("log = %q, want none", log)
	}
}

func TestRecordVarSourcesWithoutTextsStoresNone(t *testing.T) {
	// A worker older than the texts sends neither key: every row is
	// replaced without a text, so an earlier text is never shown as the
	// current commit's.
	store := previousRead()
	store.texts = map[string]repos.PlaybookText{"a.yml": text("old")}
	if _, log := record(t, store, time.Now(), encodeReport(t, reportOf("a.yml"))); log != "" {
		t.Fatalf("log = %q", log)
	}
	assertTexts(t, store.texts, map[string]repos.PlaybookText{})
}

func TestRecordVarSourcesChecksTheTextLimitsAgain(t *testing.T) {
	full := strings.Repeat("x", varsource.MaxFileBytes-1024)
	texts := map[string]any{
		"over.yml": strings.Repeat("x", varsource.MaxFileBytes+1),
		"nul.yml":  "- hosts: all\x00\n",
	}
	paths := []string{"over.yml", "nul.yml"}
	// Nine texts of 511 KiB pass the budget by one; a small one after them
	// still fits in the 8 KiB the first eight leave.
	for _, p := range []string{"p1.yml", "p2.yml", "p3.yml", "p4.yml", "p5.yml", "p6.yml", "p7.yml", "p8.yml", "p9.yml"} {
		texts[p] = full
		paths = append(paths, p)
	}
	texts["small.yml"] = playText
	paths = append(paths, "small.yml")

	store := &sourcesStore{fakeStore: newFakeStore()}
	recordTexts(t, store, reportOf(paths...), encodeTexts(t, texts, nil))
	want := map[string]repos.PlaybookText{
		"over.yml": leftOut(varsource.TextTooLarge), "nul.yml": leftOut(varsource.TextNotUTF8),
		"p9.yml": leftOut(varsource.TextBudget), "small.yml": text(playText),
	}
	for _, p := range paths[2:10] {
		want[p] = text(full)
	}
	assertTexts(t, store.texts, want)
}

func TestRecordVarSourcesChargesTheBudgetTheEscapedText(t *testing.T) {
	// "<" travels as <, six bytes: 400 KiB of it costs 2.4 MiB, so the
	// second such text passes the 4 MiB budget although its raw bytes fit.
	escaped := strings.Repeat("<", 400<<10)
	store := &sourcesStore{fakeStore: newFakeStore()}
	recordTexts(t, store, reportOf("a.yml", "b.yml"), encodeTexts(t, map[string]any{"a.yml": escaped, "b.yml": escaped}, nil))
	assertTexts(t, store.texts, map[string]repos.PlaybookText{"a.yml": text(escaped), "b.yml": leftOut(varsource.TextBudget)})
}

func TestRecordVarSourcesIgnoresMalformedTexts(t *testing.T) {
	store := &sourcesStore{fakeStore: newFakeStore()}
	log := recordTexts(t, store, reportOf("a.yml", "b.yml"), json.RawMessage(
		`{"playbook_texts": {"a.yml": 1}, "playbook_texts_left_out": {"b.yml": "too_large"}}`))
	// The texts do not decode: none is stored, the reasons still are, and
	// the sources are recorded all the same.
	assertTexts(t, store.texts, map[string]repos.PlaybookText{"b.yml": leftOut(varsource.TextTooLarge)})
	if store.replaced != 1 || !strings.Contains(log, "playbook texts") {
		t.Fatalf("replaced %d, log %q", store.replaced, log)
	}
}

func TestRecordVarSourcesKeepsThePreviousTextsOfAFailedRead(t *testing.T) {
	store := previousRead()
	store.texts = map[string]repos.PlaybookText{"a.yml": text("old a"), "b.yml": leftOut(varsource.TextTooLarge)}
	failed := varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{},
		Notes: []varsource.Note{varsource.ReadFailed("no playbooks read")}}
	recordTexts(t, store, failed, encodeTexts(t, map[string]any{"a.yml": "new a"}, map[string]any{}))
	assertTexts(t, store.texts, map[string]repos.PlaybookText{"a.yml": text("old a"), "b.yml": leftOut(varsource.TextTooLarge)})
}

func TestRecordVarSourcesKeepsTheTextOfALeftOutPlaybook(t *testing.T) {
	// A cap left b.yml unread: it keeps its previous row, text included;
	// c.yml is gone from the repository and takes its text along.
	store := previousRead()
	store.texts = map[string]repos.PlaybookText{"a.yml": text("old a"), "b.yml": text("old b"), "c.yml": text("old c")}
	r := reportOf("a.yml")
	r.Incomplete, r.LeftOut = true, []string{"b.yml"}
	recordTexts(t, store, r, encodeTexts(t, map[string]any{"a.yml": "new a"}, map[string]any{}))
	assertTexts(t, store.texts, map[string]repos.PlaybookText{"a.yml": text("new a"), "b.yml": text("old b")})
}

func TestRecordVarSourcesNamesWhyAKeptPlaybookHasNoText(t *testing.T) {
	// A cap left b.yml and c.yml unread. b.yml's row had a reason, which it
	// keeps; c.yml's row had neither text nor reason (stored by a worker
	// older than the texts), so it reads as a file this sync did not read,
	// not as "not sent". d.yml was never stored: it has no row at all.
	store := previousRead()
	store.texts = map[string]repos.PlaybookText{"b.yml": leftOut(varsource.TextTooLarge), "c.yml": {}}
	r := reportOf("a.yml")
	r.Incomplete, r.LeftOut = true, []string{"b.yml", "c.yml", "d.yml"}
	recordTexts(t, store, r, encodeTexts(t, map[string]any{"a.yml": "new a"}, map[string]any{}))
	assertTexts(t, store.texts, map[string]repos.PlaybookText{
		"a.yml": text("new a"), "b.yml": leftOut(varsource.TextTooLarge), "c.yml": leftOut(varsource.TextUnreadable),
	})
}

func TestRecordVarSourcesAPathInBothTextMapsKeepsTheText(t *testing.T) {
	store := &sourcesStore{fakeStore: newFakeStore()}
	recordTexts(t, store, reportOf("a.yml"), encodeTexts(t,
		map[string]any{"a.yml": playText}, map[string]any{"a.yml": varsource.TextTooLarge}))
	assertTexts(t, store.texts, map[string]repos.PlaybookText{"a.yml": text(playText)})
}

func TestRecordVarSourcesAReadPathIsNeverRecordedAsLeftOut(t *testing.T) {
	// A forged report names a.yml both read and left out. The read wins:
	// the record must not list it as left out, or its fresh text would be
	// served as one kept from an earlier sync.
	store := previousRead()
	r := reportOf("a.yml")
	r.Incomplete, r.LeftOut = true, []string{"a.yml", "b.yml"}
	recordTexts(t, store, r, encodeTexts(t, map[string]any{"a.yml": "new a"}, map[string]any{}))
	if got := store.record.LeftOut; len(got) != 1 || got[0] != "b.yml" {
		t.Fatalf("record left out = %q, want [b.yml]", got)
	}
}
