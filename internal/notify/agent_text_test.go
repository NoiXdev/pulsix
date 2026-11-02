package notify

import (
	"strings"
	"testing"
)

// TestAlertItemsTextDefusesAnAgentsItemName is backlog 7.42. An item is
// whatever the monitored host's agent reported — a mount point, a unit
// name — and the text form of alerts.items goes into chat messages, where
// "@everyone", "<!channel>", "<https://evil|text>" and "[text](url)" are a
// mention or a disguised link, and bidi or other invisible format
// characters can make the line read as something it is not. The item must
// reach the message without any of that taking effect.
func TestAlertItemsTextDefusesAnAgentsItemName(t *testing.T) {
	for _, item := range []string{
		"/mnt/@everyone",
		"/mnt/<!channel>",
		"/mnt/<https://evil.example|click>",
		"/mnt/[reset password](https://evil.example)",
		"/mnt/\u202egnp.exe",
		"/mnt/zero\u200bwidth",
	} {
		got := alertItemsText([]AlertItem{{Severity: "crit", HostName: "web-1", Event: "firing", CheckKey: "disk", Item: item, Metric: "disk.used_pct"}})
		for _, bad := range []string{"@", "<", ">", "[", "]", "\u202e", "\u200b"} {
			if strings.Contains(got, bad) {
				t.Errorf("item %q renders as %q, still carrying %q", item, got, bad)
			}
		}
		if !strings.Contains(got, "/mnt/") {
			t.Errorf("item %q lost its readable part: %q", item, got)
		}
	}
	// An ordinary item is left exactly as it is.
	if got := alertItemsText([]AlertItem{{Severity: "warn", HostName: "web-1", Event: "firing", CheckKey: "disk", Item: "/var/lib/docker", Metric: "disk.used_pct"}}); !strings.Contains(got, "disk//var/lib/docker disk.used_pct") {
		t.Errorf("an ordinary item changed: %q", got)
	}
}
