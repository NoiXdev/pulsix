package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f runFixture) triggersURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/triggers"
}

func (f runFixture) reposURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/repos"
}

// createRepo adds a repository through the API (operator role) and returns
// its id, for tests that need a real, existing repo_id — build() validates
// the repository exists on create, unlike the immutability check on update
// (TestTriggerUpdateCannotChangeKindOrTarget below), which rejects a
// changed target before ever looking it up.
func (f runFixture) createRepo(t *testing.T, name string) string {
	t.Helper()
	resp := postJSON(t, f.operatorC, f.reposURL(), map[string]any{"name": name, "url": "https://git.example.com/" + name + ".git"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var repo struct {
		Repo struct {
			ID string `json:"id"`
		} `json:"repo"`
	}
	decodeInto(t, resp, &repo)
	return repo.Repo.ID
}

type triggerCreated struct {
	Trigger struct {
		ID              string `json:"id"`
		Preset          string `json:"preset"`
		SignatureHeader string `json:"signature_header"`
		URL             string `json:"url"`
		Revision        int64  `json:"revision"`
	} `json:"trigger"`
	Secret string `json:"secret"`
	URL    string `json:"url"`
}

func TestTriggerLifecycle(t *testing.T) {
	f := newRunFixture(t, "trig-life")
	c := f.operatorC
	resp := postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github",
		"mapping": map[string]any{"extra_vars": map[string]string{"deploy_tag": "$.ref"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created triggerCreated
	decodeInto(t, resp, &created)
	if created.Secret == "" || created.Trigger.SignatureHeader != "X-Hub-Signature-256" || created.URL != created.Trigger.URL {
		t.Fatalf("created = %+v", created)
	}
	id := created.Trigger.ID
	// The secret is never read back.
	for _, url := range []string{f.triggersURL() + "?template_id=" + f.templateID.String(), f.triggersURL() + "/" + id} {
		if body := mustReadBody(t, requestJSON(t, f.viewerC, http.MethodGet, url, nil)); strings.Contains(body, created.Secret) || strings.Contains(body, `"secret"`) {
			t.Fatalf("%s leaks the secret: %s", url, body)
		}
	}
	// Test call: signed in-process, recorded as a delivery.
	resp = postJSON(t, c, f.triggersURL()+"/"+id+"/test", map[string]any{"payload": map[string]any{"ref": "v9"}})
	var test struct {
		Outcome string `json:"outcome"`
		RunID   string `json:"run_id"`
	}
	decodeInto(t, resp, &test)
	if test.Outcome != "triggered" || test.RunID == "" {
		t.Fatalf("test call = %+v", test)
	}
	var deliveries struct {
		Deliveries []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id+"/deliveries", nil), &deliveries)
	if len(deliveries.Deliveries) != 1 || deliveries.Deliveries[0].Reason != "test call" {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	// Rotation: the old secret stops working at the hook.
	var rotated triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL()+"/"+id+"/rotate-secret", nil), &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotated = %+v", rotated)
	}
	// Update needs If-Match.
	up := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github", "enabled": false}
	if resp := requestJSON(t, c, http.MethodPut, f.triggersURL()+"/"+id, up); resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", resp.StatusCode)
	}
	resp = requestWithHeaders(t, c, http.MethodPut, f.triggersURL()+"/"+id, up, map[string]string{"If-Match": `"2"`})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodDelete, f.triggersURL()+"/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	for _, action := range []string{"POST createTrigger", "POST testTrigger", "POST rotateTriggerSecret", "PUT updateTrigger", "DELETE deleteTrigger"} {
		assertActivityContains(t, f, action)
	}
}

func TestTriggerPermissionsAndValidation(t *testing.T) {
	f := newRunFixture(t, "trig-perm")
	body := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "t", "preset": "pulsix"}
	if resp := postJSON(t, f.viewerC, f.triggersURL(), body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list without a target filter: %d", resp.StatusCode)
	}
	bad := map[string]any{"kind": "template_run", "template_id": f.templateID.String(), "name": "t2", "preset": "pulsix",
		"mapping": map[string]any{"extra_vars": map[string]string{"x": "$..ref"}}}
	if resp := postJSON(t, f.operatorC, f.triggersURL(), bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("descendant path: %d", resp.StatusCode)
	}
	var created triggerCreated
	decodeInto(t, postJSON(t, f.operatorC, f.triggersURL(), body), &created)
	if resp := postJSON(t, f.viewerC, f.triggersURL()+"/"+created.Trigger.ID+"/rotate-secret", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer rotate: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL()+"/"+created.Trigger.ID+"/deliveries", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer deliveries: %d", resp.StatusCode)
	}
}

// TestTriggerSecretIsNeverLeaked is the brief's "the webhook secret is
// returned only in the create and rotate responses, never in get or list"
// rule (task-11-brief.md), checked the same way
// TestScheduleExtraVarsSecretsAreNeverLeaked (schedule_handlers_test.go)
// checks schedule overrides: the secret value itself, and the "secret"
// JSON key that would carry it, must be absent from get, list and the
// org's audit trail — only the create and rotate-secret responses may ever
// carry it, and only once each.
func TestTriggerSecretIsNeverLeaked(t *testing.T) {
	f := newRunFixture(t, "trig-secret")
	c := f.operatorC
	var created triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "deploy on push", "preset": "github",
	}), &created)
	id := created.Trigger.ID
	if created.Secret == "" {
		t.Fatalf("create did not return a secret: %+v", created)
	}

	var rotated triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL()+"/"+id+"/rotate-secret", nil), &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotate did not return a fresh secret: %+v", rotated)
	}

	assertNoLeak := func(t *testing.T, label, raw string) {
		t.Helper()
		for _, marker := range []string{created.Secret, rotated.Secret, `"secret"`} {
			if strings.Contains(raw, marker) {
				t.Errorf("%s leaks %q: %s", label, marker, raw)
			}
		}
	}

	assertNoLeak(t, "get", mustReadBody(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id, nil)))
	assertNoLeak(t, "list", mustReadBody(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"?template_id="+f.templateID.String(), nil)))

	activity := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=100", nil))
	for _, marker := range []string{created.Secret, rotated.Secret} {
		if strings.Contains(activity, marker) {
			t.Errorf("activity feed leaks %q: %s", marker, activity)
		}
	}
}

// TestRepoSyncTriggerLifecycle is TestTriggerLifecycle's repo_sync
// counterpart (fix round 1, Important 1): the brief's second supported
// kind ("closes 2.8's push webhook", task-11-brief.md:3) had no coverage
// at all before this — triggerPerm's repos.read/repos.write branches, the
// ?repo_id= list filter, and rotate/test/deliveries/delete for a repo
// trigger were entirely unverified.
func TestRepoSyncTriggerLifecycle(t *testing.T) {
	f := newRunFixture(t, "trig-repo-life")
	repoID := f.createRepo(t, "infra")
	c := f.operatorC

	resp := postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "repo_sync", "repo_id": repoID, "name": "sync on push", "preset": "gitlab",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created triggerCreated
	decodeInto(t, resp, &created)
	if created.Secret == "" || created.Trigger.SignatureHeader != "X-Gitlab-Token" || created.URL != created.Trigger.URL {
		t.Fatalf("created = %+v", created)
	}
	id := created.Trigger.ID

	// The ?repo_id= list filter (the ?template_id= half is covered by
	// TestTriggerLifecycle's leak check above).
	var list struct {
		Triggers []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"triggers"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"?repo_id="+repoID, nil), &list)
	if len(list.Triggers) != 1 || list.Triggers[0].ID != id || list.Triggers[0].Kind != "repo_sync" {
		t.Fatalf("repo_id list = %+v", list)
	}

	var got struct {
		Trigger struct {
			Kind   string `json:"kind"`
			RepoID string `json:"repo_id"`
		} `json:"trigger"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id, nil), &got)
	if got.Trigger.Kind != "repo_sync" || got.Trigger.RepoID != repoID {
		t.Fatalf("get = %+v", got)
	}

	// Test call: a repo_sync trigger needs no jobs.run (that gate is
	// template_run-only, trigger_handlers.go), and still starts a real
	// sync, recorded as a delivery.
	resp = postJSON(t, c, f.triggersURL()+"/"+id+"/test", map[string]any{})
	var test struct {
		Outcome string `json:"outcome"`
		RunID   string `json:"run_id"`
	}
	decodeInto(t, resp, &test)
	if test.Outcome != "triggered" || test.RunID == "" {
		t.Fatalf("test call = %+v", test)
	}
	var deliveries struct {
		Deliveries []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"deliveries"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.triggersURL()+"/"+id+"/deliveries", nil), &deliveries)
	if len(deliveries.Deliveries) != 1 || deliveries.Deliveries[0].Reason != "test call" {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	var rotated triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL()+"/"+id+"/rotate-secret", nil), &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotated = %+v", rotated)
	}

	if resp := requestJSON(t, c, http.MethodDelete, f.triggersURL()+"/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	for _, action := range []string{"POST createTrigger", "POST testTrigger", "POST rotateTriggerSecret", "DELETE deleteTrigger"} {
		assertActivityContains(t, f, action)
	}
}

// TestRepoSyncTriggerPermissions is TestTriggerPermissionsAndValidation's
// repo_sync counterpart (fix round 1, Important 1): a viewer — who has
// neither templates.write nor repos.write — is refused write access to a
// repo trigger exactly as to a template one, while read (deliveries) is
// still allowed.
//
// The coordinator's fix-round message also asked for the reverse
// direction: an operator with repos.write but without templates.write (or
// vice versa) being unable to manage the other kind. That fixture cannot
// be built: internal/tenancy/roles.go's matrix grants templates.write and
// repos.write together, at the same minimum role (operator) — no role or
// bearer-token scope in this codebase separates them, which the review
// itself already noted ("given the current single-role-scale model...
// this is provably safe, not just believed so"). What this test and
// TestTriggerPermissionsAndValidation together verify instead is the
// property that actually matters for that concern: loadTrigger
// (trigger_handlers.go) re-checks the *trigger's own stored* Kind against
// triggerPerm, so a repo trigger is gated by repos.* and a template
// trigger by templates.* — never the other way around, and never by
// whatever kind the caller's request body claims.
func TestRepoSyncTriggerPermissions(t *testing.T) {
	f := newRunFixture(t, "trig-repo-perm")
	repoID := f.createRepo(t, "infra")
	body := map[string]any{"kind": "repo_sync", "repo_id": repoID, "name": "sync", "preset": "bearer"}
	if resp := postJSON(t, f.viewerC, f.triggersURL(), body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create repo trigger: %d", resp.StatusCode)
	}
	var created triggerCreated
	decodeInto(t, postJSON(t, f.operatorC, f.triggersURL(), body), &created)
	if resp := postJSON(t, f.viewerC, f.triggersURL()+"/"+created.Trigger.ID+"/rotate-secret", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer rotate repo trigger: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.triggersURL()+"/"+created.Trigger.ID+"/deliveries", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer deliveries: %d", resp.StatusCode)
	}
}

// TestTriggerCrossOrgAccessIs404 is the IDOR case the coordinator's
// fix-round message asked for: a trigger id from another org. f2's own
// admin — a legitimate member of f2, just not of f1 — addresses f1's
// trigger id through f2's own org route; org scoping (Store.Get is
// filtered by orgID) must answer 404, the same as an unknown id, rather
// than leaking that the id exists in some other org.
func TestTriggerCrossOrgAccessIs404(t *testing.T) {
	f1 := newRunFixture(t, "trig-org-a")
	f2 := newRunFixture(t, "trig-org-b")
	var created triggerCreated
	decodeInto(t, postJSON(t, f1.operatorC, f1.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f1.templateID.String(), "name": "t", "preset": "pulsix",
	}), &created)

	foreignURL := f2.triggersURL() + "/" + created.Trigger.ID
	if resp := requestJSON(t, f2.adminC, http.MethodGet, foreignURL, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org get: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, f2.adminC, foreignURL+"/rotate-secret", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org rotate: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f2.adminC, http.MethodGet, foreignURL+"/deliveries", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org deliveries: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestTriggerUpdateCannotChangeKindOrTarget is the other IDOR-adjacent
// check the coordinator's fix-round message asked for: an update must not
// be able to re-target a trigger (kind, template_id or repo_id) — internal/
// triggers.Service.Update already enforces this (it compares the request
// against the stored trigger before ever calling build, so the replacement
// id need not even exist for the check to fire); this pins that behaviour
// at the HTTP layer, where the review flagged it as unverified.
func TestTriggerUpdateCannotChangeKindOrTarget(t *testing.T) {
	f := newRunFixture(t, "trig-immut")
	c := f.operatorC
	var created triggerCreated
	decodeInto(t, postJSON(t, c, f.triggersURL(), map[string]any{
		"kind": "template_run", "template_id": f.templateID.String(), "name": "t", "preset": "pulsix",
	}), &created)
	url := f.triggersURL() + "/" + created.Trigger.ID
	ifMatch := map[string]string{"If-Match": `"1"`}

	changeKind := map[string]any{"kind": "repo_sync", "repo_id": uuid.NewString(), "name": "t", "preset": "pulsix"}
	if resp := requestWithHeaders(t, c, http.MethodPut, url, changeKind, ifMatch); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("kind change: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	changeTarget := map[string]any{"kind": "template_run", "template_id": uuid.NewString(), "name": "t", "preset": "pulsix"}
	if resp := requestWithHeaders(t, c, http.MethodPut, url, changeTarget, ifMatch); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("target change: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Confirms the 400s above really did nothing: the trigger is still
	// reachable, unmodified, at its original revision.
	var got struct {
		Trigger struct {
			Revision int64 `json:"revision"`
		} `json:"trigger"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, url, nil), &got)
	if got.Trigger.Revision != created.Trigger.Revision {
		t.Fatalf("revision changed: got %d, want %d", got.Trigger.Revision, created.Trigger.Revision)
	}
}
