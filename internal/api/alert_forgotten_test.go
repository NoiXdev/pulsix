package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestAForgottenAlertReadsAsForgotten (backlog 3.12b): an alert closed by
// forgetting its item comes back with closed_reason "forgotten" and its
// closed event, and the document advertises the value — the SPA's
// generated types are built from that enum, and a reason missing from it
// is a value the GUI has no label for.
func TestAForgottenAlertReadsAsForgotten(t *testing.T) {
	f := newAlertFixture(t, "alert-forgotten")
	opened := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	id := f.seedOpenAlert(t, f.hostA, tupleSubject(f.hostA, "disk", "/data", "used_percent"), rules.StateWarn, opened)

	ctx := context.Background()
	tx, err := f.s.DB.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n, err := alerts.NewService(alerts.NewSQLStore(f.s.DB), time.Now).
		CloseForgotten(ctx, tx, f.orgID, f.hostA, "disk", "/data", opened.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("close forgotten = %d, %v; want 1, nil", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	resp, detail := f.getAlert(t, f.viewerC, id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if detail.ClosedAt == nil || detail.ClosedReason != "forgotten" {
		t.Fatalf("closed_at/closed_reason = %v/%q, want set/forgotten", detail.ClosedAt, detail.ClosedReason)
	}
	last := detail.Timeline[len(detail.Timeline)-1]
	if last.Event == nil || last.Event.Kind != "closed" {
		t.Fatalf("last timeline entry = %+v, want the closed event", last)
	}

	spec := mustReadBody(t, requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/openapi.json", nil))
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []any `json:"enum"` // numbers too (retention_days)
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatal(err)
	}
	enum := doc.Components.Schemas["AlertDTO"].Properties["closed_reason"].Enum
	for _, want := range []string{"resolved", "stale", "forgotten"} {
		if !slices.Contains(enum, any(want)) {
			t.Errorf("AlertDTO.closed_reason enum = %v, missing %q", enum, want)
		}
	}
}
