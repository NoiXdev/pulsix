package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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

// TestRunURLEmptyWithoutRun is the re-review's Minor: "run.url" aliases
// the event's top-level URL, but that link is the run's — it must not
// leak through for an event that carries no Run section (e.g. a skipped
// or failed decision), the same rule every other run.* field already
// follows.
func TestRunURLEmptyWithoutRun(t *testing.T) {
	ev := sampleEventForKind(EventScheduleSkipped)
	if ev.Run != nil {
		t.Fatal("test setup: this kind must have no Run section")
	}
	if ev.URL == "" {
		t.Fatal("test setup: the event must still carry a top-level URL")
	}
	body, err := renderWebhookBody(`{"u": {{ run.url | json }}, "text": "{{ run.url }}"}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["u"] != nil || doc["text"] != "" {
		t.Fatalf("run.url without a Run = %+v (%v), want null/empty", doc, err)
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

// TestCheckBodyTemplateTruncatesInvalidPlaceholderInErrorMessage is the
// re-review's regression for a save-time 422 whose body an operator's own
// mistyped placeholder could blow up: the error used to echo the whole
// inner text between "{{" and "}}" verbatim, so a template with one huge
// malformed placeholder produced an error message of the same size.
func TestCheckBodyTemplateTruncatesInvalidPlaceholderInErrorMessage(t *testing.T) {
	huge := strings.Repeat("x ", 8000) // ~16000 bytes, not a valid path
	err := checkBodyTemplate("{{"+huge+"}}", false)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("checkBodyTemplate = %v, want ErrInvalidInput", err)
	}
	if n := len(err.Error()); n > 200 {
		t.Fatalf("error message is %d bytes long, want the placeholder text truncated: %s", n, err.Error()[:120])
	}
}

// TestCheckBodyTemplateRejectsPathologicalInputs regression-tests the
// malformed-template shapes the re-review's probe exercised directly
// against parseBodyTemplate/checkBodyTemplate (nested "{{", a template of
// nothing but open braces, and a large run of unterminated placeholders):
// every one of them must be a clean, fast save-time rejection, never a
// hang or a panic.
func TestCheckBodyTemplateRejectsPathologicalInputs(t *testing.T) {
	cases := map[string]string{
		"nested braces":       "{{ {{ run.result }} }}",
		"triple braces":       "{{{ run.result }}}",
		"only open braces":    strings.Repeat("{", 16<<10),
		"many unclosed pairs": strings.Repeat("{{", 4096),
	}
	for name, bad := range cases {
		if err := checkBodyTemplate(bad, false); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: checkBodyTemplate = %v, want ErrInvalidInput", name, err)
		}
	}
}

// TestRenderPlaceholderEscapesHostileValuesToValidJSON regression-tests
// the re-review's escaping probe directly: control characters, a
// script-injection-shaped string, quotes and backslashes all come out as
// valid, round-tripping JSON in both the default and "| json" forms.
func TestRenderPlaceholderEscapesHostileValuesToValidJSON(t *testing.T) {
	ev := sampleEvent()
	ev.Schedule.Name = "</script><!-- \x01\x1f \r\n\t \"quoted\" back\\slash ☃"
	body, err := renderWebhookBody(`{"text": "{{ schedule.name }}", "j": {{ schedule.name | json }}}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(body) {
		t.Fatalf("not valid JSON: %s", body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["text"] != ev.Schedule.Name || doc["j"] != ev.Schedule.Name {
		t.Fatalf("doc = %+v (%v)", doc, err)
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

// TestDigestPeriodLabelMatchesOrgZone is fix round 1, Important #1:
// digestPeriod must put *both* ends of the week in the organization's own
// zone, not just from — otherwise the renderer's bare Time.Format prints
// to's UTC date, and the "Scheduled runs from … to … (zone)" line either
// undercounts or overcounts the week by a day for an organization whose
// local date differs from the UTC date at the due instant. Two zones on
// opposite sides of UTC, both far enough from it to disagree with UTC on
// the date at the occurrence:
//   - Pacific/Auckland (UTC+12 in July, NZ winter/NZST): a Monday 08:00
//     due time is still Sunday in UTC, so a to still carrying occurrence's
//     UTC Location would render one day short of the true local date —
//     a six-day week.
//   - America/Los_Angeles (UTC-7 in July, PDT): a 20:00 due time has
//     already rolled into the next UTC day, so a to still in UTC would
//     render one day past the true local date — an eight-day week.
func TestDigestPeriodLabelMatchesOrgZone(t *testing.T) {
	for _, tc := range []struct {
		zone     string
		local    time.Time // the occurrence, expressed in zone
		wantFrom string    // "2 Jan" of the local from date
		wantTo   string    // "2 Jan 2006" of the local to date
	}{
		{
			zone:     "Pacific/Auckland",
			local:    time.Date(2026, 7, 6, 8, 0, 0, 0, mustLoadLocation(t, "Pacific/Auckland")), // Mon 6 Jul, NZST
			wantFrom: "29 Jun", wantTo: "6 Jul 2026",
		},
		{
			zone:     "America/Los_Angeles",
			local:    time.Date(2026, 7, 6, 20, 0, 0, 0, mustLoadLocation(t, "America/Los_Angeles")), // Mon 6 Jul, PDT
			wantFrom: "29 Jun", wantTo: "6 Jul 2026",
		},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			from, to, err := digestPeriod(tc.local.UTC(), tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			// The DigestSummary values: both ends must report the local
			// calendar date the organization actually sees, which a
			// UTC-located to would get wrong for exactly these zones.
			if got := to.Format("2 Jan 2006"); got != tc.wantTo {
				t.Fatalf("to = %s, want %s (org-local date)", got, tc.wantTo)
			}
			if got := from.Format("2 Jan"); got != tc.wantFrom {
				t.Fatalf("from = %s, want %s (org-local date)", got, tc.wantFrom)
			}
			if to.Sub(from) != 7*24*time.Hour {
				t.Fatalf("to - from = %s, want exactly 7*24h for a week with no DST change in it", to.Sub(from))
			}

			// The rendered label: the e-mail body must read a true
			// seven-day span in the org's own dates, not the UTC ones.
			ev := sampleEvent()
			ev.Kind, ev.Digest.From, ev.Digest.To, ev.Digest.Timezone = EventDigest, from, to, tc.zone
			_, body := renderEmail(ev)
			wantLine := fmt.Sprintf("Scheduled runs from %s to %s (%s)", tc.wantFrom, tc.wantTo, tc.zone)
			if !strings.Contains(body, wantLine) {
				t.Fatalf("body does not contain %q:\n%s", wantLine, body)
			}
		})
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
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
