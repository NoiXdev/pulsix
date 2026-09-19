package notify

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRenderWebhookBodyDefaultIsTheEventJSON(t *testing.T) {
	ev := sampleEvent()
	body, err := renderWebhookBody("", ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(body, &back); err != nil || back.Run.HostsFailed != 1 || back.Schedule.Name != "Nightly OS patching" {
		t.Fatalf("default body = %s (%v)", body, err)
	}
}

func TestRenderWebhookBodyDefaultBoundedBySize(t *testing.T) {
	ev := sampleEvent()
	ev.Org.Name = strings.Repeat("x", maxBodyBytes+1)
	if _, err := renderWebhookBody("", ev); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("oversized default body = %v, want errBodyTooLarge", err)
	}
}

// TestRenderPlaceholderDefaultEscapesThroughJSON: the plain "{{ path }}"
// form renders the field's content already JSON-escaped, so it drops
// straight into a template-authored string literal.
func TestRenderPlaceholderDefaultEscapesThroughJSON(t *testing.T) {
	ev := sampleEvent()
	ev.Schedule.Name = `quote " and newline` + "\n"
	body, err := renderWebhookBody(`{"text": "Run for {{ schedule.name }}", "failed": {{ run.hosts_failed | json }}}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body %s is not valid JSON (%v)", body, err)
	}
	if doc["text"] != "Run for "+ev.Schedule.Name || doc["failed"] != float64(1) {
		t.Fatalf("doc = %+v", doc)
	}
}

// TestRenderPlaceholderJSONSuffixEmitsACompleteValue: "| json" renders a
// quoted string, a bare number, or null — never something that needs the
// author to add quotes.
func TestRenderPlaceholderJSONSuffixEmitsACompleteValue(t *testing.T) {
	ev := sampleEvent()
	body, err := renderWebhookBody(`{"id": {{ schedule.id | json }}, "count": {{ run.hosts_failed | json }}}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["id"] != ev.Schedule.ID.String() || doc["count"] != float64(1) {
		t.Fatalf("doc = %+v (%v)", doc, err)
	}
}

// TestRenderPlaceholderMissingFieldNeverFails: a path whose section is nil
// for this event kind renders as an empty string (default) or null
// ("| json"), never an error — this is what makes save-time validation
// against every event kind meaningful (Important 1 of the review).
func TestRenderPlaceholderMissingFieldNeverFails(t *testing.T) {
	ev := sampleEventForKind(EventScheduleSkipped) // Run is nil for this kind
	body, err := renderWebhookBody(`{"result": {{ run.result | json }}, "text": "{{ run.result }}"}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["result"] != nil || doc["text"] != "" {
		t.Fatalf("doc = %+v (%v)", doc, err)
	}
}

func TestRenderWebhookBodyPlaceholderBoundedBySize(t *testing.T) {
	ev := sampleEvent()
	ev.Org.Name = strings.Repeat("x", maxBodyBytes+1)
	if _, err := renderWebhookBody(`{{ org.name | json }}`, ev); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("oversized rendered body = %v, want errBodyTooLarge", err)
	}
}

func TestCheckBodyTemplate(t *testing.T) {
	if err := checkBodyTemplate("", false); err != nil {
		t.Fatal(err)
	}
	if err := checkBodyTemplate(`{"s": {{ schedule.name | json }}}`, true); err != nil {
		t.Fatal(err)
	}
	if err := checkBodyTemplate(strings.Repeat("x", maxTemplateLength+1), false); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("an oversized template was accepted")
	}
}

func TestCheckBodyTemplateRejectsUnknownAndBadSyntax(t *testing.T) {
	cases := []string{
		`{{ schedule.title }}`, // not a real field
		`{{ }}`,                // empty
		`{{ run.result`,        // unterminated
		`{{ if run.result }}`,  // no control flow
	}
	for _, bad := range cases {
		if err := checkBodyTemplate(bad, false); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("checkBodyTemplate(%q) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

// TestCheckBodyTemplateRejectsMethodLikeSyntax: the placeholder grammar
// has no method calls, arguments or field chains beyond the allowlist, so
// nothing resembling Go template method syntax parses (Important 2 of the
// review — the engine no longer executes methods at all).
func TestCheckBodyTemplateRejectsMethodLikeSyntax(t *testing.T) {
	cases := []string{
		`{{ run.result() }}`,
		`{{ .Schedule.ID.Scan "ZZZZ" }}`,
		`{{ run.result | json | json }}`,
		`{{ range run }}{{ end }}`,
		`{{ schedule.name.Scan }}`,
	}
	for _, bad := range cases {
		if err := checkBodyTemplate(bad, false); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("checkBodyTemplate(%q) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

// TestCheckBodyTemplateAcceptsFieldsAbsentForSomeKinds is the direct fix
// for Important 1: a template using a field that only run.finished events
// carry must still validate, because the engine renders it empty/null for
// every other kind rather than failing.
func TestCheckBodyTemplateAcceptsFieldsAbsentForSomeKinds(t *testing.T) {
	if err := checkBodyTemplate(`{"result": {{ run.result | json }}, "outcome": {{ decision.outcome | json }}}`, true); err != nil {
		t.Fatalf("a field absent for some event kinds must still validate: %v", err)
	}
}

// TestCheckBodyTemplateValidatesJSONWhenContentTypeSaysSo covers the
// "Content-Type: json" save-time check: malformed JSON is only rejected
// when the channel declares a JSON body.
func TestCheckBodyTemplateValidatesJSONWhenContentTypeSaysSo(t *testing.T) {
	bad := `{"name": {{ schedule.name }}}` // unquoted: schedule.name has a space
	if err := checkBodyTemplate(bad, false); err != nil {
		t.Fatalf("a non-JSON channel must not enforce JSON validity: %v", err)
	}
	if err := checkBodyTemplate(bad, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("checkBodyTemplate(%q, true) = %v, want ErrInvalidInput", bad, err)
	}
}

// TestCheckBodyTemplateValidatesHostileContent is the cheap guard from
// review Minor 1: even though default rendering always JSON-escapes, save
// time also renders against names carrying quotes, backslashes and
// newlines to catch a future escaping regression before it ships.
func TestCheckBodyTemplateValidatesHostileContent(t *testing.T) {
	if err := checkBodyTemplate(`{"name": "{{ schedule.name }}"}`, true); err != nil {
		t.Fatalf("hostile-content JSON validation should still pass a correctly escaped template: %v", err)
	}
}

func TestRenderEmail(t *testing.T) {
	ev := sampleEvent()
	subject, body := renderEmail(ev)
	if subject != "[Pulsix] Nightly OS patching: failed" || !strings.Contains(body, "Hosts:     12 (ok 11, changed 9, failed 1)") || !strings.Contains(body, ev.URL) {
		t.Fatalf("run email = %q\n%s", subject, body)
	}
	ev.Run.Result = "succeeded"
	if subject, _ = renderEmail(ev); subject != "[Pulsix] Nightly OS patching: succeeded with changes" {
		t.Fatalf("succeeded with changes = %q", subject)
	}
	ev.Kind = EventScheduleSkipped
	if subject, body = renderEmail(ev); subject != "[Pulsix] Nightly OS patching: skipped" || !strings.Contains(body, "skipped_maintenance") {
		t.Fatalf("skipped email = %q\n%s", subject, body)
	}
	ev.Kind = EventScheduleFailed
	if subject, _ = renderEmail(ev); subject != "[Pulsix] Nightly OS patching: could not start" {
		t.Fatalf("failed decision email = %q", subject)
	}
	ev.Kind = EventDigest
	if subject, body = renderEmail(ev); subject != "[Pulsix] Weekly schedule digest for Example" || !strings.Contains(body, "Nightly OS patching: 2 run(s), 1 failure(s), 1 skipped or missed") || !strings.Contains(body, "Slowest runs:") {
		t.Fatalf("digest email = %q\n%s", subject, body)
	}
	ev.Digest.Schedules, ev.Digest.Slowest = nil, nil
	if _, body = renderEmail(ev); !strings.Contains(body, "No schedules.") {
		t.Fatalf("empty digest = %s", body)
	}
	ev.Kind = EventTest
	if subject, _ = renderEmail(ev); subject != "[Pulsix] Test notification from Example" {
		t.Fatalf("test email = %q", subject)
	}
}

func TestEventCategories(t *testing.T) {
	if got := runCategories("failed", 3); len(got) != 2 || got[0] != CategoryFailure || got[1] != CategoryChange {
		t.Fatalf("failed with changes = %v", got)
	}
	if got := runCategories("cancelled", 0); len(got) != 1 || got[0] != CategoryFailure {
		t.Fatalf("cancelled = %v", got)
	}
	if got := runCategories("succeeded", 0); len(got) != 1 || got[0] != CategorySuccess {
		t.Fatalf("succeeded = %v", got)
	}
	for outcome, want := range map[string]EventKind{"skipped_overlap": EventScheduleSkipped, "skipped_maintenance": EventScheduleSkipped, "missed": EventScheduleSkipped, "failed": EventScheduleFailed} {
		if _, kind, ok := decisionCategory(outcome); !ok || kind != want {
			t.Errorf("decisionCategory(%s) = %s, %v", outcome, kind, ok)
		}
	}
	if _, _, ok := decisionCategory("triggered"); ok {
		t.Fatal("a triggered decision is reported when its run finishes, not now")
	}
	if c, ok := ParseCategory("change"); !ok || c != CategoryChange {
		t.Fatal("ParseCategory(change)")
	}
	if _, ok := ParseCategory("always"); ok {
		t.Fatal("ParseCategory accepted an unknown category")
	}
}
