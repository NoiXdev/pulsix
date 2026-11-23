// The repo sync's playbook texts (backlog 3.127): what readPlaybookTexts
// keeps and leaves out, and that the executor ships them only to a manager
// that asked for them.
package worker

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/varsource"
)

const textPlay = "- hosts: all\n  tasks: []\n"

// textsOf reads paths of the clone at dir (subpath "") through the clone's
// read root.
func textsOf(t *testing.T, dir string, paths ...string) (map[string]string, map[string]string) {
	t.Helper()
	fsys, err := openCloneFS(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsys.Close() }()
	return readPlaybookTexts(context.Background(), fsys, paths)
}

func TestReadPlaybookTextsKeepsEveryReadableText(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"site.yml":        textPlay,
		"deploy/web.yaml": "- hosts: web\n  roles: [nginx]\n",
		"empty.yml":       "",
	})
	texts, left := textsOf(t, clone, "site.yml", "deploy/web.yaml", "empty.yml")
	want := map[string]string{"site.yml": textPlay, "deploy/web.yaml": "- hosts: web\n  roles: [nginx]\n", "empty.yml": ""}
	if !reflect.DeepEqual(texts, want) || len(left) != 0 {
		t.Fatalf("texts = %q, left out = %q; want %q and none", texts, left, want)
	}
}

func TestReadPlaybookTextsLeavesOutAFileOverThePerFileLimit(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"big.yml":   strings.Repeat("a", varsource.MaxFileBytes+1),
		"limit.yml": strings.Repeat("b", varsource.MaxFileBytes),
	})
	texts, left := textsOf(t, clone, "big.yml", "limit.yml")
	if _, ok := texts["big.yml"]; ok || left["big.yml"] != varsource.TextTooLarge {
		t.Fatalf("big.yml: left out = %q, want %q", left["big.yml"], varsource.TextTooLarge)
	}
	if len(texts["limit.yml"]) != varsource.MaxFileBytes {
		t.Fatalf("a file of exactly %d bytes is left out (%q)", varsource.MaxFileBytes, left["limit.yml"])
	}
}

func TestReadPlaybookTextsFillsTheBudgetInPathOrder(t *testing.T) {
	clone := t.TempDir()
	full := strings.Repeat("x", varsource.MaxFileBytes)
	files := map[string]string{"small.yml": textPlay}
	var paths []string
	// Nine files of 512 KiB pass the 4 MiB budget by one file; a small one
	// after them still fits beside the first eight.
	for _, name := range []string{"p1.yml", "p2.yml", "p3.yml", "p4.yml", "p5.yml", "p6.yml", "p7.yml", "p8.yml", "p9.yml"} {
		files[name] = full
		paths = append(paths, name)
	}
	writeTree(t, clone, files)
	// The small one first would take budget the eighth large one needs.
	texts, left := textsOf(t, clone, append(paths, "small.yml")...)
	for _, p := range paths[:8] {
		if texts[p] != full {
			t.Fatalf("%s: left out %q, want it kept (8 x 512 KiB = 4 MiB fits)", p, left[p])
		}
	}
	if left["p9.yml"] != varsource.TextBudget {
		t.Fatalf("p9.yml: left out = %q, want %q", left["p9.yml"], varsource.TextBudget)
	}
	if _, ok := texts["p9.yml"]; ok {
		t.Fatal("p9.yml is both kept and left out")
	}
	// 4 MiB are taken exactly: even the small text is over the budget now.
	if left["small.yml"] != varsource.TextBudget {
		t.Fatalf("small.yml: left out = %q, want %q", left["small.yml"], varsource.TextBudget)
	}

	// A small file after one that did not fit is kept when it still fits.
	texts, left = textsOf(t, clone, "p1.yml", "p2.yml", "p3.yml", "p4.yml", "p5.yml", "p6.yml", "p7.yml", "small.yml", "p8.yml")
	if texts["small.yml"] != textPlay || left["p8.yml"] != varsource.TextBudget {
		t.Fatalf("small.yml kept = %v, p8.yml left out = %q; want the small one kept, p8 over the budget",
			texts["small.yml"] == textPlay, left["p8.yml"])
	}
}

// The budget bounds what the result carries, so a text counts as its JSON
// encoding does: characters the encoder escapes cost more than one byte.
func TestReadPlaybookTextsChargesTheBudgetWhatTheResultCarries(t *testing.T) {
	clone := t.TempDir()
	// "<" travels as <, six bytes: 512 KiB of them cost 3 MiB.
	escaped := strings.Repeat("<", varsource.MaxFileBytes)
	writeTree(t, clone, map[string]string{"a.yml": escaped, "b.yml": escaped})
	texts, left := textsOf(t, clone, "a.yml", "b.yml")
	if texts["a.yml"] != escaped || left["b.yml"] != varsource.TextBudget {
		t.Fatalf("a.yml kept = %v, b.yml left out = %q; want a kept, b over the budget", texts["a.yml"] == escaped, left["b.yml"])
	}
	raw, err := json.Marshal(texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > varsource.MaxPlaybookTextsBytes+64 {
		t.Fatalf("the encoded texts are %d bytes, past the %d byte budget", len(raw), varsource.MaxPlaybookTextsBytes)
	}
}

func TestReadPlaybookTextsLeavesOutWhatIsNoUTF8Text(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"latin1.yml": "- hosts: all\n  vars: {name: \"M\xfcller\"}\n",
		"nul.yml":    "- hosts: all\x00\n",
		"utf8.yml":   "- hosts: all\n  vars: {name: \"Müller\"}\n",
	})
	texts, left := textsOf(t, clone, "latin1.yml", "nul.yml", "utf8.yml")
	want := map[string]string{"latin1.yml": varsource.TextNotUTF8, "nul.yml": varsource.TextNotUTF8}
	if !reflect.DeepEqual(left, want) || len(texts) != 1 || texts["utf8.yml"] == "" {
		t.Fatalf("texts = %q, left out = %q; want only utf8.yml kept, the rest %q", texts, left, want)
	}
}

func TestReadPlaybookTextsNeverLeavesTheReadRoot(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"secret.yml": "- hosts: all # outside\n"})
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		".git/config.yml": "- hosts: all # git\n",
		"real.yml":        textPlay,
	})
	for name, target := range map[string]string{
		"abs.yml":      filepath.Join(outside, "secret.yml"),
		"up.yml":       "../" + filepath.Base(outside) + "/secret.yml",
		"intogit.yml":  ".git/config.yml",
		"inside.yml":   "real.yml",
		"dangling.yml": "nowhere.yml",
	} {
		if err := os.Symlink(target, filepath.Join(clone, name)); err != nil {
			t.Fatal(err)
		}
	}
	texts, left := textsOf(t, clone,
		"abs.yml", "up.yml", "intogit.yml", ".git/config.yml", ".GIT/config.yml",
		"dangling.yml", "missing.yml", "/etc/passwd.yml", "../x.yml", "no-yaml.txt", "inside.yml")
	wantLeft := map[string]string{}
	for _, p := range []string{"abs.yml", "up.yml", "intogit.yml", ".git/config.yml", ".GIT/config.yml",
		"dangling.yml", "missing.yml", "/etc/passwd.yml", "../x.yml", "no-yaml.txt"} {
		wantLeft[p] = varsource.TextUnreadable
	}
	if !reflect.DeepEqual(left, wantLeft) {
		t.Fatalf("left out = %q, want %q", left, wantLeft)
	}
	// A link that stays inside the root is followed.
	if !reflect.DeepEqual(texts, map[string]string{"inside.yml": textPlay}) {
		t.Fatalf("texts = %q, want only inside.yml", texts)
	}
}

// openCounter records the names fs.FS.Open is called with.
type openCounter struct {
	fs.FS
	opened []string
}

// Stat keeps fs.Stat from falling back to Open, which would count a stat.
func (o *openCounter) Stat(name string) (fs.FileInfo, error) { return fs.Stat(o.FS, name) }

func (o *openCounter) Open(name string) (fs.File, error) {
	o.opened = append(o.opened, name)
	return o.FS.Open(name)
}

func TestReadPlaybookTextsDoesNotOpenAFileThatCannotFitTheBudget(t *testing.T) {
	clone := t.TempDir()
	full := strings.Repeat("x", varsource.MaxFileBytes)
	files := map[string]string{"small.yml": textPlay}
	var paths []string
	for _, name := range []string{"p1.yml", "p2.yml", "p3.yml", "p4.yml", "p5.yml", "p6.yml", "p7.yml", "p8.yml", "p9.yml", "p10.yml"} {
		files[name] = full
		paths = append(paths, name)
	}
	writeTree(t, clone, files)
	// Eight files take the whole 4 MiB: the two after them are left out as
	// over the budget from their size alone, and so is the small one, as
	// no byte is left.
	counter := &openCounter{FS: os.DirFS(clone)}
	texts, left := readPlaybookTexts(context.Background(), counter, append(paths, "small.yml"))
	if len(texts) != 8 || left["p9.yml"] != varsource.TextBudget || left["p10.yml"] != varsource.TextBudget ||
		left["small.yml"] != varsource.TextBudget {
		t.Fatalf("texts = %d, left out = %q; want 8 kept, the rest over the budget", len(texts), left)
	}
	if len(counter.opened) != 8 {
		t.Fatalf("opened %q, want only the eight kept files (a full budget needs no read)", counter.opened)
	}

	// With room left, a file that does not fit is not opened but a later
	// one that does still is.
	counter = &openCounter{FS: os.DirFS(clone)}
	texts, left = readPlaybookTexts(context.Background(), counter, []string{"p1.yml", "p2.yml", "p3.yml", "p4.yml", "p5.yml", "p6.yml", "p7.yml", "small.yml", "p9.yml"})
	if texts["small.yml"] != textPlay || left["p9.yml"] != varsource.TextBudget || len(texts) != 8 {
		t.Fatalf("texts = %d, left out = %q; want small.yml kept", len(texts), left)
	}
	for _, name := range counter.opened {
		if name == "p9.yml" {
			t.Fatalf("p9.yml was opened although it cannot fit (%q)", counter.opened)
		}
	}
}

func TestReadPlaybookTextsStopsWhenTheContextIsDone(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"a.yml": textPlay, "b.yml": textPlay})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	counter := &openCounter{FS: os.DirFS(clone)}
	texts, left := readPlaybookTexts(ctx, counter, []string{"a.yml", "b.yml"})
	if len(counter.opened) != 0 || len(texts) != 0 || texts == nil || left == nil {
		t.Fatalf("opened %q, texts %q, left out %q; want nothing read and two non-nil maps", counter.opened, texts, left)
	}
}

func TestReadTextsConfinesAnInnerSubpath(t *testing.T) {
	// The read root is the subpath: a link inside it that leaves it, even
	// to a file that is inside the clone, is unreadable.
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"ansible/site.yml":    textPlay,
		"ansible/sub/own.yml": textPlay,
		"other/outside.yml":   "- hosts: all # outside the subpath\n",
		"top.yml":             "- hosts: all # clone root\n",
	})
	for name, target := range map[string]string{
		"ansible/to-other.yml": "../other/outside.yml",
		"ansible/to-top.yml":   "../top.yml",
		"ansible/to-site.yml":  "site.yml",
	} {
		if err := os.Symlink(target, filepath.Join(clone, name)); err != nil {
			t.Fatal(err)
		}
	}
	e := &SyncExecutor{Log: slog.New(slog.DiscardHandler)}
	texts, left := e.readTexts(context.Background(), clone, "ansible",
		[]string{"site.yml", "sub/own.yml", "to-other.yml", "to-top.yml", "to-site.yml", "../top.yml", "../other/outside.yml"})
	wantLeft := map[string]string{
		"to-other.yml": varsource.TextUnreadable, "to-top.yml": varsource.TextUnreadable,
		"../top.yml": varsource.TextUnreadable, "../other/outside.yml": varsource.TextUnreadable,
	}
	if !reflect.DeepEqual(left, wantLeft) {
		t.Fatalf("left out = %q, want %q", left, wantLeft)
	}
	if want := map[string]string{"site.yml": textPlay, "sub/own.yml": textPlay, "to-site.yml": textPlay}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

func TestReadPlaybookTextsNamesEveryPathOnce(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"site.yml": textPlay})
	texts, left := textsOf(t, clone)
	if texts == nil || left == nil || len(texts)+len(left) != 0 {
		t.Fatalf("no paths: texts = %#v, left out = %#v; want two empty maps", texts, left)
	}
	texts, left = textsOf(t, clone, "site.yml", "site.yml")
	if len(texts) != 1 || len(left) != 0 {
		t.Fatalf("a repeated path: texts = %q, left out = %q", texts, left)
	}
}

// syncTexts runs a sync of clone (with subpath) and returns its stats;
// parse stands in for the read of the sources unless nil.
func syncTexts(t *testing.T, clone, subpath string, want bool, parse parseRepoFunc) map[string]any {
	t.Helper()
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}, Log: slog.New(slog.DiscardHandler), parseRepo: parse}
	job := syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", subpath, "")
	job.WantPlaybookTexts = want
	c := &evCollector{}
	status, stats, err := e.Run(context.Background(), job, RunEnv{WorkDir: t.TempDir(), RepoCacheDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	// The run view's event never carries the texts.
	for _, ev := range c.events {
		if strings.Contains(ev.GetPayload(), varsource.PlaybookTextsStatsKey) {
			t.Fatalf("event payload carries the texts: %s", ev.GetPayload())
		}
	}
	return stats
}

// shippedTexts is the result's texts and left-out paths, both present.
func shippedTexts(t *testing.T, stats map[string]any) (map[string]string, map[string]string) {
	t.Helper()
	texts, ok := stats[varsource.PlaybookTextsStatsKey].(map[string]string)
	if !ok || texts == nil {
		t.Fatalf("stats[%s] = %#v", varsource.PlaybookTextsStatsKey, stats[varsource.PlaybookTextsStatsKey])
	}
	left, ok := stats[varsource.PlaybookTextsLeftOutStatsKey].(map[string]string)
	if !ok || left == nil {
		t.Fatalf("stats[%s] = %#v", varsource.PlaybookTextsLeftOutStatsKey, stats[varsource.PlaybookTextsLeftOutStatsKey])
	}
	return texts, left
}

func TestSyncExecutorShipsTextsOnlyWhenAsked(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"ansible/site.yml":     textPlay,
		"ansible/vars/x.yml":   "a: 1\n",
		"outside/ignored.yml":  textPlay,
		"ansible/notes/x.yaml": "- hosts: db\n",
	})

	// An older manager never sets the flag: neither key reaches it.
	stats := syncTexts(t, clone, "ansible", false, nil)
	for _, key := range []string{varsource.PlaybookTextsStatsKey, varsource.PlaybookTextsLeftOutStatsKey} {
		if _, ok := stats[key]; ok {
			t.Fatalf("a claim without want_playbook_texts got stats[%s]", key)
		}
	}
	if _, ok := stats[VarSourcesStatsKey]; !ok {
		t.Fatal("the variable sources are gone")
	}

	// Asked, the texts are the report's playbooks under the subpath.
	stats = syncTexts(t, clone, "ansible", true, nil)
	texts, left := shippedTexts(t, stats)
	var r varsource.Report
	if err := json.Unmarshal(stats[VarSourcesStatsKey].(json.RawMessage), &r); err != nil {
		t.Fatal(err)
	}
	if got, want := reportPaths(r), []string{"notes/x.yaml", "site.yml"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("report paths = %q, want %q", got, want)
	}
	want := map[string]string{"notes/x.yaml": "- hosts: db\n", "site.yml": textPlay}
	if !reflect.DeepEqual(texts, want) || len(left) != 0 {
		t.Fatalf("texts = %q, left out = %q; want %q", texts, left, want)
	}
}

func TestSyncExecutorShipsATextOrAReasonForEveryReportedPath(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"site.yml":   textPlay,
		"failed.yml": "- hosts: all\n  roles: [deep]\n",
		"big.yml":    strings.Repeat("#", varsource.MaxFileBytes+1),
		"extra.yml":  textPlay,
	})
	// The report names a playbook it could not read (failed), one whose
	// file is gone since, one over the per-file limit; extra.yml is no
	// playbook of the report.
	parse := func(context.Context, fs.FS, varsource.Installed, string) varsource.Report {
		return varsource.Report{Commit: testCommit, Notes: []varsource.Note{}, Playbooks: []varsource.Playbook{
			{Path: "site.yml"}, {Path: "failed.yml", Failed: true}, {Path: "gone.yml"}, {Path: "big.yml"},
		}}
	}
	texts, left := shippedTexts(t, syncTexts(t, clone, "", true, parse))
	wantTexts := map[string]string{"site.yml": textPlay, "failed.yml": "- hosts: all\n  roles: [deep]\n"}
	wantLeft := map[string]string{"gone.yml": varsource.TextUnreadable, "big.yml": varsource.TextTooLarge}
	if !reflect.DeepEqual(texts, wantTexts) || !reflect.DeepEqual(left, wantLeft) {
		t.Fatalf("texts = %q, left out = %q; want %q and %q", texts, left, wantTexts, wantLeft)
	}
}

func TestSyncExecutorShipsEmptyTextsWithAFailedReport(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"site.yml": textPlay})
	// A subpath that cannot be opened: the report is Failed, with no
	// playbooks, and the texts name none either.
	stats := syncTexts(t, clone, "missing", true, nil)
	if texts, _ := stats[varsource.PlaybookTextsStatsKey].(map[string]string); texts == nil || len(texts) != 0 {
		t.Fatalf("stats[%s] = %#v, want an empty map", varsource.PlaybookTextsStatsKey, stats[varsource.PlaybookTextsStatsKey])
	}
	if left, _ := stats[varsource.PlaybookTextsLeftOutStatsKey].(map[string]string); left == nil || len(left) != 0 {
		t.Fatalf("stats[%s] = %#v, want an empty map", varsource.PlaybookTextsLeftOutStatsKey, stats[varsource.PlaybookTextsLeftOutStatsKey])
	}
}

func TestSyncExecutorShipsNoTextsWithoutAReport(t *testing.T) {
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{"site.yml": textPlay})
	// A commit no report can carry: not even the Failed report encodes, so
	// the sync sends no sources, and no texts either.
	e := &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: strings.Repeat("a", varsource.MaxReportBytes)}, Log: slog.New(slog.DiscardHandler)}
	job := syncClaim(t, "01900000-0000-7000-8000-000000000123", "https://git.example.org/x.git", "main", "", "")
	job.WantPlaybookTexts = true
	c := &evCollector{}
	status, stats, err := e.Run(context.Background(), job, RunEnv{WorkDir: t.TempDir(), RepoCacheDir: t.TempDir()}, c.sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	for _, key := range []string{VarSourcesStatsKey, varsource.PlaybookTextsStatsKey, varsource.PlaybookTextsLeftOutStatsKey} {
		if _, ok := stats[key]; ok {
			t.Fatalf("a sync without a report carries stats[%s]", key)
		}
	}
}
