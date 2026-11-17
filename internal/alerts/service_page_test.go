package alerts_test

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/paging"
	"github.com/noixdev/pulsix/internal/rules"
)

type pageCall struct {
	filter alerts.Filter
	mute   alerts.MuteQuery
	page   paging.Page
}

// pageStore wraps the fake with the two offset-paging reads. ListAlertsPage
// applies the muted predicate the way the SQL does, from the MuteQuery
// Service hands it, so a wrong Now or host list shows up as a wrong page.
type pageStore struct {
	*fakeStore
	calls   []pageCall
	hostIDs []uuid.UUID // OpenAlertHostIDs' answer; nil derives it from the open alerts
	hostErr error
}

func (p *pageStore) OpenAlertHostIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	if p.hostErr != nil {
		return nil, p.hostErr
	}
	if p.hostIDs != nil {
		return p.hostIDs, nil
	}
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, a := range p.alerts {
		if a.ClosedAt == nil && !seen[a.Subject.HostID] {
			seen[a.Subject.HostID] = true
			out = append(out, a.Subject.HostID)
		}
	}
	return out, nil
}

func (p *pageStore) ListAlertsPage(_ context.Context, _ uuid.UUID, f alerts.Filter, m alerts.MuteQuery, pg paging.Page) ([]alerts.AlertWithHost, int, error) {
	p.calls = append(p.calls, pageCall{f, m, pg})
	var rows []alerts.AlertWithHost
	for _, a := range p.alerts {
		if f.Muted != nil {
			muted := a.ClosedAt == nil && ((a.SnoozedUntil != nil && a.SnoozedUntil.After(m.Now)) || a.SnoozeUntilChange || slices.Contains(m.HostIDs, a.Subject.HostID))
			if muted != *f.Muted {
				continue
			}
		}
		rows = append(rows, alerts.AlertWithHost{Alert: a, HostName: p.hostNames[a.Subject.HostID]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].OpenedAt.After(rows[j].OpenedAt) })
	total := len(rows)
	lo := min(pg.Offset(), total)
	hi := min(lo+pg.Size, total)
	return rows[lo:hi], total, nil
}

func newPageStore() *pageStore { return &pageStore{fakeStore: newFakeStore()} }

func TestListPageMutedHostsComeFromMute(t *testing.T) {
	st := newPageStore()
	now := t0.Add(time.Hour)
	maint, silence, other, plain := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for i, h := range []uuid.UUID{maint, silence, other, plain} {
		a := openAlert(alerts.Subject{HostID: h, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "u"}, rules.StateWarn, t0.Add(time.Duration(i)*time.Minute))
		st.alerts[a.ID] = a
	}
	covering := func(r maintenance.Reason) []maintenance.Occurrence {
		return []maintenance.Occurrence{{WindowID: uuid.Must(uuid.NewV7()), Reason: r, Start: t0, End: now.Add(time.Hour)}}
	}
	st.windows[maint] = covering(maintenance.ReasonMaintenance)
	st.windows[silence] = covering(maintenance.ReasonSilence)
	st.windows[other] = covering(maintenance.Reason("something-else")) // does not mute, same verdict as Mute
	st.windows[plain] = []maintenance.Occurrence{{WindowID: uuid.Must(uuid.NewV7()), Reason: maintenance.ReasonMaintenance, Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}

	svc := alerts.NewService(st, clockAt(now))
	yes, no := true, false
	got, total, err := svc.ListPage(ctx, orgID, alerts.Filter{Muted: &yes}, paging.Page{Number: 1, Size: 10, Sort: "opened", Desc: true})
	if err != nil || total != 2 || len(got) != 2 {
		t.Fatalf("muted=true = %d rows, total %d, %v; want 2/2", len(got), total, err)
	}
	for _, v := range got {
		if !v.Suppression.Muted {
			t.Errorf("row on host %s carries no suppression", v.Subject.HostID)
		}
	}
	call := st.calls[0]
	if len(call.mute.HostIDs) != 2 || !slices.Contains(call.mute.HostIDs, maint) || !slices.Contains(call.mute.HostIDs, silence) || !call.mute.Now.Equal(now) {
		t.Fatalf("mute query = %+v, want hosts {maint, silence} at %v", call.mute, now)
	}
	got, total, err = svc.ListPage(ctx, orgID, alerts.Filter{Muted: &no}, paging.Page{Number: 1, Size: 10, Sort: "opened", Desc: true})
	if err != nil || total != 2 || len(got) != 2 {
		t.Fatalf("muted=false = %d rows, total %d, %v; want 2/2", len(got), total, err)
	}
}

func TestListPageWithoutMutedFilterSkipsWindowLookups(t *testing.T) {
	st := newPageStore()
	a := openAlert(alerts.Subject{HostID: uuid.Must(uuid.NewV7()), Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "u"}, rules.StateWarn, t0)
	st.alerts[a.ID] = a
	svc := alerts.NewService(st, clockAt(t0.Add(time.Hour)))
	got, total, err := svc.ListPage(ctx, orgID, alerts.Filter{}, paging.Page{Number: 1, Size: 10, Sort: "opened", Desc: true})
	if err != nil || total != 1 || len(got) != 1 {
		t.Fatalf("ListPage = %d/%d, %v", len(got), total, err)
	}
	// Rows still get their suppression, one window lookup per distinct host.
	if len(st.windowCalls) != 1 {
		t.Fatalf("window lookups = %d, want 1 (the row's own host)", len(st.windowCalls))
	}
	if len(st.calls[0].mute.HostIDs) != 0 {
		t.Fatalf("host ids passed without a muted filter: %v", st.calls[0].mute.HostIDs)
	}
}

func TestListPageMutedFilterErrorsPropagate(t *testing.T) {
	st := newPageStore()
	st.hostErr = context.DeadlineExceeded
	yes := true
	_, _, err := alerts.NewService(st, clockAt(t0)).ListPage(ctx, orgID, alerts.Filter{Muted: &yes}, paging.Page{Number: 1, Size: 10, Sort: "opened"})
	if err == nil {
		t.Fatal("want an error")
	}
}

// TestListPageLabelsUseTheFilterSnapshot: the windows the filter used are the
// ones the labels use, even if the answer changes between reads, and each host
// is fetched once.
func TestListPageLabelsUseTheFilterSnapshot(t *testing.T) {
	st := newPageStore()
	now := t0.Add(time.Hour)
	h := uuid.Must(uuid.NewV7())
	a := openAlert(alerts.Subject{HostID: h, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "u"}, rules.StateWarn, t0)
	st.alerts[a.ID] = a
	st.windows[h] = []maintenance.Occurrence{{WindowID: uuid.Must(uuid.NewV7()), Reason: maintenance.ReasonMaintenance, Start: t0, End: now.Add(time.Hour)}}
	ws := &changingWindows{pageStore: st, h: h}
	yes := true
	got, total, err := alerts.NewService(ws, clockAt(now)).ListPage(ctx, orgID, alerts.Filter{Muted: &yes}, paging.Page{Number: 1, Size: 10, Sort: "opened", Desc: true})
	if err != nil || total != 1 || len(got) != 1 || !got[0].Suppression.Muted {
		t.Fatalf("got %d rows (total %d, err %v), want one labelled muted", len(got), total, err)
	}
	if ws.n != 1 {
		t.Fatalf("WindowsForHost called %d times, want 1", ws.n)
	}
}

// changingWindows answers WindowsForHost with a covering window the first
// time and none afterwards.
type changingWindows struct {
	*pageStore
	h uuid.UUID
	n int
}

func (c *changingWindows) WindowsForHost(ctx context.Context, org, host uuid.UUID, at time.Time) ([]maintenance.Occurrence, error) {
	c.n++
	if c.n > 1 {
		return nil, nil
	}
	return c.pageStore.WindowsForHost(ctx, org, host, at)
}

func TestListPageMutedLookupScope(t *testing.T) {
	st := newPageStore()
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, h := range []uuid.UUID{h1, h2} {
		a := openAlert(alerts.Subject{HostID: h, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "u"}, rules.StateWarn, t0)
		st.alerts[a.ID] = a
	}
	svc := alerts.NewService(st, clockAt(t0.Add(time.Hour)))
	yes, closed := true, false
	pg := paging.Page{Number: 1, Size: 10, Sort: "opened", Desc: true}
	if _, _, err := svc.ListPage(ctx, orgID, alerts.Filter{Muted: &yes, HostID: &h1}, pg); err != nil {
		t.Fatal(err)
	}
	if len(st.windowCalls) != 1 || st.windowCalls[0] != h1 {
		t.Fatalf("host filter: window lookups %v, want only %s", st.windowCalls, h1)
	}
	st.windowCalls = nil
	if _, _, err := svc.ListPage(ctx, orgID, alerts.Filter{Muted: &yes, Open: &closed}, pg); err != nil {
		t.Fatal(err)
	}
	if len(st.windowCalls) != 0 {
		t.Fatalf("closed-only: %d window lookups, want 0", len(st.windowCalls))
	}
}
