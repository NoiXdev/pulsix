package alerts_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestNotSeenSince (backlog 3.57): the "not seen since …" moment belongs
// to an open absent warning alone — the host-scope alert at severity
// warn — and is the host's agent's last contact as read now, not stored
// on the alert. A closed warning has none: the agent's last contact has
// moved on since (the client came back), so it would no longer say when
// the client went away.
func TestNotSeenSince(t *testing.T) {
	seen := t0.Add(-15 * 24 * time.Hour)
	closedAt := t0
	closed := openAlert(hostSubject(), rules.StateWarn, t0)
	closed.ClosedAt, closed.ClosedReason = &closedAt, alerts.Resolved

	cases := []struct {
		name    string
		a       alerts.Alert
		contact time.Time
		want    *time.Time
	}{
		{"an open absent warning", openAlert(hostSubject(), rules.StateWarn, t0), seen, &seen},
		{"an open absent warning whose host has no agent any more", openAlert(hostSubject(), rules.StateWarn, t0), time.Time{}, nil},
		{"a closed absent warning", closed, seen, nil},
		{"the unknown host alert", openAlert(hostSubject(), rules.StateUnknown, t0), seen, nil},
		{"a warn tuple alert", openAlert(tupleSubject("disk", "/", "used_percent"), rules.StateWarn, t0), seen, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := alerts.NotSeenSince(alerts.AlertWithHost{Alert: tc.a, AgentLastContact: tc.contact})
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("NotSeenSince = %v, want none", *got)
			case tc.want != nil && (got == nil || !got.Equal(*tc.want)):
				t.Fatalf("NotSeenSince = %v, want %v", got, *tc.want)
			}
		})
	}
	if !alerts.IsAbsentWarning(openAlert(hostSubject(), rules.StateWarn, t0)) {
		t.Fatal("a host-scope warn alert is not the absent warning")
	}
	if alerts.IsAbsentWarning(openAlert(hostSubject(), rules.StateUnknown, t0)) {
		t.Fatal("the unknown host alert reads as the absent warning")
	}
}

// TestListAndGetCarryNotSeenSince: Service hands the moment to the API on
// both reads, so the list and the alert page say the same thing.
func TestListAndGetCarryNotSeenSince(t *testing.T) {
	store := newFakeStore()
	seen := t0.Add(-15 * 24 * time.Hour)
	warn := openAlert(hostSubject(), rules.StateWarn, t0)
	store.alerts[warn.ID] = warn
	store.agentContacts[hostID] = seen
	svc := alerts.NewService(store, clockAt(t0.Add(time.Hour)))

	list, _, err := svc.List(ctx, orgID, alerts.Filter{}, "", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].NotSeenSince == nil || !list[0].NotSeenSince.Equal(seen) {
		t.Fatalf("List = %+v, want the warning not seen since %v", list, seen)
	}
	detail, err := svc.Get(ctx, orgID, warn.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.NotSeenSince == nil || !detail.NotSeenSince.Equal(seen) {
		t.Fatalf("Get NotSeenSince = %v, want %v", detail.NotSeenSince, seen)
	}
}
