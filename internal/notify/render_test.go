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

func TestRenderWebhookBodyTemplateEscapesThroughJSON(t *testing.T) {
	ev := sampleEvent()
	ev.Schedule.Name = `quote " and newline` + "\n"
	body, err := renderWebhookBody(`{"text": {{json .Schedule.Name}}, "failed": {{.Run.HostsFailed}}}`, ev)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["text"] != ev.Schedule.Name || doc["failed"] != float64(1) {
		t.Fatalf("body %s is not the expected JSON (%v)", body, err)
	}
}

func TestRenderWebhookBodyLimits(t *testing.T) {
	if _, err := renderWebhookBody(`{{printf "%070000d" 1}}`, sampleEvent()); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("oversized body = %v", err)
	}
	if _, err := renderWebhookBody(`{{.Run.Nope}}`, sampleEvent()); err == nil {
		t.Fatal("an unknown field rendered")
	}
	if _, err := renderWebhookBody(`{{`, sampleEvent()); err == nil {
		t.Fatal("an unparsable template rendered")
	}
}

func TestCheckBodyTemplate(t *testing.T) {
	if err := checkBodyTemplate(""); err != nil {
		t.Fatal(err)
	}
	if err := checkBodyTemplate(`{"s": {{json .Schedule.Name}}}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{{.Schedule.Title}}`, `{{ if }}`, strings.Repeat("x", maxTemplateLength+1)} {
		if err := checkBodyTemplate(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("checkBodyTemplate(%.20q) = %v", bad, err)
		}
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
