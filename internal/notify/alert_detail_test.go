package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The alert detail (backlog 3.13b): why an alert fired, rendered after the
// subject in the e-mail body and the alerts.items text, and carried as
// "detail" by alerts.items | json. Parts of it come from the monitored
// site (the value an assertion saw), so the text form defuses it like an
// agent's item name (backlog 7.42) — Review Focus 3.

func detailBatch(items ...AlertItem) Event {
	return Event{
		Kind: EventAlertBatch, At: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC),
		Org:    OrgRef{Name: "Acme"},
		Alerts: &AlertBatch{Count: len(items), Items: items},
	}
}

func TestAlertItemsTextRendersTheDetailAfterTheSubject(t *testing.T) {
	got := alertItemsText([]AlertItem{
		{Severity: "crit", HostName: "shop-1", Event: "opened", CheckKey: "shop", Item: "home", Metric: "http.assertion", Detail: `status == 200 — saw 503`},
		{Severity: "crit", HostName: "shop-1", Event: "opened", CheckKey: "shop", Metric: "http.up", Detail: "timeout"},
		{Severity: "warn", HostName: "db-1", Event: "opened", CheckKey: "disk", Item: "/", Metric: "disk.used_pct"},
	})
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), got)
	}
	for i, want := range []string{
		"shop/home http.assertion: status == 200 — saw 503",
		"shop http.up: timeout",
		"disk// disk.used_pct",
	} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want it to end in %q", i, lines[i], want)
		}
	}
}

func TestRenderEmailCarriesTheDetail(t *testing.T) {
	_, body := renderEmail(detailBatch(AlertItem{
		Severity: "crit", HostName: "shop-1", Event: "opened", CheckKey: "shop", Metric: "http.up", Detail: "certificate",
	}))
	if !strings.Contains(body, "shop http.up: certificate") {
		t.Errorf("body does not say why:\n%s", body)
	}
}

// A hostile actual — what a monitored page put in its title — reaches a
// chat message without a mention, a Slack control sequence, a disguised
// link, a line break or a bidi override taking effect, through the e-mail
// body and the alerts.items text alike.
func TestTheDetailIsDefusedInText(t *testing.T) {
	for _, detail := range []string{
		`html("title") contains "Shop" — saw "@everyone"`,
		`html("title") contains "Shop" — saw "<!channel>"`,
		`html("title") contains "Shop" — saw "<https://evil.example|reset>"`,
		`html("title") contains "Shop" — saw "[login](https://evil.example)"`,
		"html(\"title\") contains \"Shop\" — saw \"gnp.\u202eexe\"",
		"html(\"title\") contains \"Shop\" — saw \"zero\u200bwidth\"",
		"timeout\ncrit     db-prod-1            opened   disk/ x",
	} {
		item := AlertItem{Severity: "crit", HostName: "shop-1", Event: "opened", CheckKey: "shop", Item: "title", Metric: "http.assertion", Detail: detail}
		_, mail := renderEmail(detailBatch(item))
		text := alertItemsText([]AlertItem{item})
		for _, got := range []string{text, mail} {
			for _, bad := range []string{"@", "<", ">", "[", "]", "\u202e", "\u200b"} {
				if strings.Contains(got, bad) {
					t.Errorf("detail %q renders as %q, still carrying %q", detail, got, bad)
				}
			}
		}
		if strings.Contains(text, "\n") {
			t.Errorf("detail %q breaks the line: %q", detail, text)
		}
		if !strings.Contains(text, "shop/title http.assertion: ") {
			t.Errorf("detail %q lost the subject: %q", detail, text)
		}
	}
}

// Webhook templates get no new placeholder: the detail reaches them in the
// alerts.items text and as "detail" in alerts.items | json — raw there,
// since a JSON consumer reads a field, not markup, and the JSON encoder
// escapes it.
func TestWebhookTemplatesCarryTheDetail(t *testing.T) {
	ev := detailBatch(
		AlertItem{Severity: "crit", HostName: "shop-1", Event: "opened", CheckKey: "shop", Item: "home", Metric: "http.assertion", Detail: `status == 200 — saw "<b>"`},
		AlertItem{Severity: "warn", HostName: "db-1", Event: "opened", CheckKey: "disk", Item: "/", Metric: "disk.used_pct"},
	)
	body, err := renderWebhookBody(`{"text": "{{ alerts.items }}", "items": {{ alerts.items | json }}}`, ev)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got struct {
		Text  string           `json:"text"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	if !strings.Contains(got.Text, `http.assertion: status == 200 — saw "‹b›"`) {
		t.Errorf("alerts.items = %q, want the detail after the subject", got.Text)
	}
	if len(got.Items) != 2 || got.Items[0]["detail"] != `status == 200 — saw "<b>"` {
		t.Fatalf("alerts.items | json = %v, want the first item's detail", got.Items)
	}
	if _, present := got.Items[1]["detail"]; present {
		t.Errorf("an item without a detail carries %v, want the field omitted", got.Items[1]["detail"])
	}
}

// The sample events templates are validated against carry a detail, the
// hostile one a hostile detail — so a template that renders the detail
// is checked against the worst it can hold.
func TestTheSampleEventsCarryADetail(t *testing.T) {
	sample := sampleEventForKind(EventAlertBatch)
	if sample.Alerts == nil || sample.Alerts.Items[0].Detail == "" {
		t.Fatalf("the sample alert batch carries no detail: %+v", sample.Alerts)
	}
	hostile := hostileSampleEventForKind(EventAlertBatch)
	for _, it := range hostile.Alerts.Items {
		if !strings.Contains(it.Detail, "\n") || !strings.Contains(it.Detail, `"`) {
			t.Errorf("hostile sample item detail = %q, want the hostile text", it.Detail)
		}
	}
	if err := checkBodyTemplate(`{"items": {{ alerts.items | json }}, "text": "{{ alerts.items }}"}`, true); err != nil {
		t.Errorf("a template over the items no longer validates: %v", err)
	}
}
