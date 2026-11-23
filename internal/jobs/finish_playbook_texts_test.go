// The playbook texts a sync result carries (backlog 3.127): the finish
// takes both keys out of the stats for every sync, whatever its outcome,
// and hands them to the source sink beside the report.
package jobs_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

const sitePlay = "- hosts: all\n  tasks: []\n"

// withTexts adds the playbook texts keys to stats as the gRPC layer hands
// them over (decoded JSON objects).
func withTexts(stats map[string]any, texts, leftOut map[string]any) map[string]any {
	stats[varsource.PlaybookTextsStatsKey] = texts
	stats[varsource.PlaybookTextsLeftOutStatsKey] = leftOut
	return stats
}

func assertNoTextsStored(t *testing.T, stored map[string]any, status jobs.Status) {
	t.Helper()
	for _, key := range []string{varsource.PlaybookTextsStatsKey, varsource.PlaybookTextsLeftOutStatsKey} {
		if _, kept := stored[key]; kept {
			t.Fatalf("a %s sync stored %s in jobs.stats: %v", status, key, stored)
		}
	}
}

func TestFinishSyncNeverStoresPlaybookTextsInStats(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-texts-strip-org")
	for _, status := range []jobs.Status{jobs.StatusSucceeded, jobs.StatusFailed, jobs.StatusCancelled} {
		j := f.runningJob(t, jobs.KindSyncRepo)
		stats, _ := workerStats(t, f.repoID, testReport())
		stats = withTexts(stats, map[string]any{"site.yml": sitePlay}, map[string]any{"big.yml": varsource.TextTooLarge})
		if err := f.queue.Finish(ctx, j.ID, status, stats, ""); err != nil {
			t.Fatalf("Finish(%s): %v", status, err)
		}
		stored := f.storedStats(t, j.ID)
		assertNoTextsStored(t, stored, status)
		if stored["repo_id"] != f.repoID.String() {
			t.Fatalf("a %s sync lost the rest of its stats: %v", status, stored)
		}
		if _, still := stats[varsource.PlaybookTextsStatsKey]; !still {
			t.Fatal("Finish must not modify the caller's stats map")
		}
	}
	// Only the succeeded sync reached the sink, with both keys.
	calls := f.sink.all()
	if len(calls) != 1 {
		t.Fatalf("sink calls = %+v, want the succeeded sync's only", calls)
	}
	var got map[string]map[string]string
	if err := json.Unmarshal(calls[0].texts, &got); err != nil {
		t.Fatalf("sink texts %s: %v", calls[0].texts, err)
	}
	if got[varsource.PlaybookTextsStatsKey]["site.yml"] != sitePlay ||
		got[varsource.PlaybookTextsLeftOutStatsKey]["big.yml"] != varsource.TextTooLarge {
		t.Fatalf("sink texts = %v", got)
	}

	// One key alone is taken out as well (a forged or truncated result).
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, testReport())
	stats[varsource.PlaybookTextsLeftOutStatsKey] = map[string]any{"site.yml": varsource.TextBudget}
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	assertNoTextsStored(t, f.storedStats(t, j.ID), jobs.StatusSucceeded)

	// A non-sync job's stats are not touched.
	other := f.runningJob(t, jobs.KindConnectionTest)
	if err := f.queue.Finish(ctx, other.ID, jobs.StatusSucceeded, map[string]any{varsource.PlaybookTextsStatsKey: "x"}, ""); err != nil {
		t.Fatalf("Finish(connection_test): %v", err)
	}
	if got := f.storedStats(t, other.ID)[varsource.PlaybookTextsStatsKey]; got != "x" {
		t.Fatalf("a non-sync job's stats = %v, want them untouched", got)
	}
}

func TestFinishSyncWithoutTextsHandsTheSinkNone(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-texts-none-org")
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, testReport())
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	calls := f.sink.all()
	if len(calls) != 1 || calls[0].raw == nil || calls[0].texts != nil {
		t.Fatalf("sink calls = %+v, want one with the report and no texts (an older worker)", calls)
	}
}

// TestFinishSyncStoresPlaybookTexts runs the finish through the real
// source sink: each reported path gets its text or its reason.
func TestFinishSyncStoresPlaybookTexts(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-texts-store-org")
	f.queue.SetSourceSink(repos.NewService(repos.NewSQLStore(f.d), nil, nil))
	report := testReport()
	report.Playbooks = append(report.Playbooks, varsource.Playbook{Path: "big.yml"}, varsource.Playbook{Path: "old.yml"})
	j := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, report)
	stats = withTexts(stats, map[string]any{"site.yml": sitePlay}, map[string]any{"big.yml": varsource.TextTooLarge})
	if err := f.queue.Finish(ctx, j.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	assertNoTextsStored(t, f.storedStats(t, j.ID), jobs.StatusSucceeded)

	store := repos.NewSQLStore(f.d)
	scoped := db.WithOrgScope(ctx, f.org)
	want := map[string]struct {
		content *string
		leftOut string
	}{
		"site.yml": {ptr(sitePlay), ""},
		"big.yml":  {nil, varsource.TextTooLarge},
		"old.yml":  {nil, ""},
	}
	for path, w := range want {
		got, err := store.GetPlaybookText(scoped, f.org, f.repoID, path)
		if err != nil {
			t.Fatalf("GetPlaybookText(%s): %v", path, err)
		}
		if !sameText(got.Content, w.content) || got.LeftOut != w.leftOut || got.Commit != report.Commit {
			t.Fatalf("%s = %+v, want content %v, left out %q", path, got, w.content, w.leftOut)
		}
	}
	if _, err := store.GetPlaybookText(scoped, f.org, uuid.Must(uuid.NewV7()), "site.yml"); err == nil {
		t.Fatal("another repository's text read")
	}
}

func ptr(s string) *string { return &s }

func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
