package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/triggers"
)

func (f runFixture) createTrigger(t *testing.T, mutate func(*triggers.Input)) triggers.Created {
	t.Helper()
	in := triggers.Input{Kind: triggers.KindTemplateRun, TemplateID: &f.templateID, Name: "hook-" + uuid.NewString()[:8], Enabled: true,
		Preset: triggers.PresetPulsix, Mapping: triggers.Mapping{ExtraVars: map[string]string{"deploy_tag": "$.ref"}}}
	if mutate != nil {
		mutate(&in)
	}
	c, err := f.s.Deps.Triggers.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.Must(uuid.NewV7()), in)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// hook posts body to url with the given headers, anonymously.
func hook(t *testing.T, url string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func signedHeaders(c triggers.Created, body []byte) map[string]string {
	return map[string]string{c.Trigger.SignatureHeader(): triggers.Sign(c.Trigger.Preset, c.Secret, "", body)}
}

func TestHookTriggersARun(t *testing.T) {
	f := newRunFixture(t, "hook-run")
	c := f.createTrigger(t, nil)
	body := []byte(`{"ref":"v1.2.3"}`)
	resp := hook(t, c.URL, body, signedHeaders(c, body))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("hook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	decodeInto(t, resp, &out)
	var job struct {
		Job struct {
			Trigger    string `json:"trigger"`
			TriggerRef string `json:"trigger_ref"`
		} `json:"job"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.baseURL()+"/"+out.RunID, nil), &job)
	if job.Job.Trigger != "webhook" || job.Job.TriggerRef != c.Trigger.ID.String() {
		t.Fatalf("job = %+v", job)
	}
	body2 := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=100", nil))
	if !strings.Contains(body2, `"webhook.run"`) || !strings.Contains(body2, `"webhook"`) {
		t.Fatalf("activity lacks the webhook run: %s", body2)
	}
}

func TestHookUniformNotFound(t *testing.T) {
	f := newRunFixture(t, "hook-404")
	body := []byte(`{"ref":"x"}`)
	open := f.createTrigger(t, nil)
	disabled := f.createTrigger(t, func(in *triggers.Input) { in.Enabled = false })
	fenced := f.createTrigger(t, func(in *triggers.Input) { in.AllowCIDRs = []string{"10.0.0.0/8"} })
	responses := map[string]*http.Response{
		"unknown id":      hook(t, f.s.HTTP.URL+"/api/v1/hooks/jobs/"+uuid.NewString(), body, signedHeaders(open, body)),
		"malformed id":    hook(t, f.s.HTTP.URL+"/api/v1/hooks/jobs/not-a-uuid", body, signedHeaders(open, body)),
		"wrong endpoint":  hook(t, f.s.HTTP.URL+"/api/v1/hooks/repos/"+open.Trigger.ID.String(), body, signedHeaders(open, body)),
		"disabled":        hook(t, disabled.URL, body, signedHeaders(disabled, body)),
		"wrong source ip": hook(t, fenced.URL, body, signedHeaders(fenced, body)),
		"bad signature":   hook(t, open.URL, body, map[string]string{"X-Pulsix-Signature-256": "sha256=deadbeef"}),
		"no signature":    hook(t, open.URL, body, nil),
	}
	var first string
	for name, resp := range responses {
		b := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
		if first == "" {
			first = b
		} else if b != first {
			t.Errorf("%s: body %q differs from %q; 404s must be indistinguishable", name, b, first)
		}
	}
}

func TestHookLimitsAndPayloadErrors(t *testing.T) {
	f := newRunFixture(t, "hook-limits")
	big := bytes.Repeat([]byte("a"), triggers.MaxBodyBytes+1)
	c := f.createTrigger(t, func(in *triggers.Input) { in.RateLimit = triggers.RateLimit{Count: 1, Per: "1h"} })
	if resp := hook(t, c.URL, big, signedHeaders(c, big)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
	notJSON := []byte(`ref=x`)
	resp := hook(t, c.URL, notJSON, signedHeaders(c, notJSON))
	if b := mustReadBody(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(b, `"body"`) || strings.Contains(b, "ref=x") {
		t.Fatalf("not json: %d %s", resp.StatusCode, b)
	}
	// The 422 above consumed the one token of the hour: the next call is 429.
	body := []byte(`{"ref":"x"}`)
	resp = hook(t, c.URL, body, signedHeaders(c, body))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
}

func TestHookOnActiveSkipAndTimestampWindow(t *testing.T) {
	f := newRunFixture(t, "hook-active")
	c := f.createTrigger(t, func(in *triggers.Input) {
		in.OnActive = triggers.OnActiveSkip
		in.TimestampHeader = "X-Pulsix-Timestamp"
	})
	body := []byte(`{"ref":"x"}`)
	signAt := func(at time.Time) map[string]string {
		ts := strconv.FormatInt(at.Unix(), 10)
		return map[string]string{"X-Pulsix-Timestamp": ts, c.Trigger.SignatureHeader(): triggers.Sign(c.Trigger.Preset, c.Secret, ts, body)}
	}
	if resp := hook(t, c.URL, body, signAt(time.Now())); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := hook(t, c.URL, body, signAt(time.Now()))
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(b), `"skipped"`) {
		t.Fatalf("second while active: %d %s", resp.StatusCode, b)
	}
	if resp := hook(t, c.URL, body, signAt(time.Now().Add(-10*time.Minute))); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("replayed: %d", resp.StatusCode)
	}
}

func TestRepoHookSyncsTheRepository(t *testing.T) {
	f := newRunFixture(t, "hook-repo")
	oc := f.operatorC
	resp := postJSON(t, oc, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/repos", map[string]any{"name": "infra", "url": "https://git.example.com/infra.git"})
	var repo struct {
		Repo struct {
			ID string `json:"id"`
		} `json:"repo"`
	}
	decodeInto(t, resp, &repo)
	repoID := uuid.MustParse(repo.Repo.ID)
	c, err := f.s.Deps.Triggers.Create(db.WithOrgScope(context.Background(), f.orgID), f.orgID, uuid.Must(uuid.NewV7()),
		triggers.Input{Kind: triggers.KindRepoSync, RepoID: &repoID, Name: "push", Enabled: true, Preset: triggers.PresetGitLab})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"object_kind":"push"}`)
	resp = hook(t, c.URL, body, map[string]string{"X-Gitlab-Token": c.Secret})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("repo hook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := hook(t, c.URL, body, map[string]string{"X-Gitlab-Token": c.Secret + "x"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
}
