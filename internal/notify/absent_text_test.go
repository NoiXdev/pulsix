package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestAlertItemsTextSaysWhenAnAbsentClientWasLastSeen (backlog 3.57): the
// absent warning — the host-scope alert at severity warn — has no check
// to name, so its line says what it is about: the client has not been
// seen since its agent's last contact. Without the moment (a closed
// warning) it still says what it was. The unknown host alert's line is
// unchanged.
func TestAlertItemsTextSaysWhenAnAbsentClientWasLastSeen(t *testing.T) {
	seen := time.Date(2026, 10, 29, 7, 42, 13, 0, time.FixedZone("CET", 3600))
	cases := []struct {
		name string
		item AlertItem
		want string
	}{
		{
			name: "an open absent warning",
			item: AlertItem{HostName: "notebook-3", Severity: "warn", Event: "opened", NotSeenSince: &seen},
			want: "warn     notebook-3           opened   not seen since 2026-10-29 06:42 UTC",
		},
		{
			name: "a closed absent warning",
			item: AlertItem{HostName: "notebook-3", Severity: "warn", Event: "closed"},
			want: "warn     notebook-3           closed   not seen",
		},
		{
			name: "the unknown host alert",
			item: AlertItem{HostName: "db-1", Severity: "unknown", Event: "opened"},
			want: "unknown  db-1                 opened   ",
		},
		{
			name: "a warn tuple alert",
			item: AlertItem{HostName: "web-2", CheckKey: "cpu", Metric: "used_percent", Severity: "warn", Event: "opened"},
			want: "warn     web-2                opened   cpu used_percent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alertItemsText([]AlertItem{tc.item}); got != tc.want {
				t.Fatalf("alertItemsText:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestAlertItemCarriesNotSeenSinceInJSON: a default webhook body (the
// event's JSON) and a template's `alerts.items | json` both carry the
// moment for an integration to word itself, and leave it out otherwise.
func TestAlertItemCarriesNotSeenSinceInJSON(t *testing.T) {
	seen := time.Date(2026, 10, 29, 6, 42, 0, 0, time.UTC)
	with, err := json.Marshal(AlertItem{Severity: "warn", NotSeenSince: &seen})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"not_seen_since":"2026-10-29T06:42:00Z"`) {
		t.Fatalf("JSON %s lacks not_seen_since", with)
	}
	without, err := json.Marshal(AlertItem{Severity: "crit", CheckKey: "disk"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "not_seen_since") {
		t.Fatalf("JSON %s carries not_seen_since for an ordinary alert", without)
	}
}
