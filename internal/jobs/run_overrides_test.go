package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

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
}
