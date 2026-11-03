package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api"
	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/workers"
)

func workerCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestWorkerEnrollmentAndRevocation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-owner@example.test", "correct horse battery")
	operator := s.SeedUser("workers-operator@example.test", "correct horse battery")
	org := s.SeedOrg("workers-org", owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	c, oc := s.LoginAs(owner), s.LoginAs(operator)
	base := s.HTTP.URL + "/api/v1/orgs/workers-org"
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}

	if resp := requestJSON(t, oc, http.MethodGet, base+"/workers", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, oc, base+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "op-1"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator create: %d", resp.StatusCode)
	}
	resp := postJSON(t, c, base+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "hel-1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	var created struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
		Token          string `json:"token"`
		DockerSnippet  string `json:"docker_snippet"`
		ComposeSnippet string `json:"compose_snippet"`
		ComposeUp      string `json:"compose_up_command"`
	}
	decodeInto(t, resp, &created)
	if !strings.HasPrefix(created.Token, "pxw1.") || !strings.Contains(created.DockerSnippet, created.Token) ||
		!strings.Contains(created.DockerSnippet, "PULSIX_MANAGER_ADDR=localhost:8444") || !strings.Contains(created.ComposeSnippet, "PULSIX_ENROLL_TOKEN") {
		t.Fatalf("created = %+v", created)
	}
	// The compose file never carries the token; the start command does (7.59).
	if strings.Contains(created.ComposeSnippet, created.Token) || created.ComposeUp != "PULSIX_ENROLL_TOKEN="+created.Token+" docker compose up -d" {
		t.Fatalf("compose snippet %q, up command %q", created.ComposeSnippet, created.ComposeUp)
	}
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/worker-enrollments", nil)); !strings.Contains(body, created.Enrollment.ID) || strings.Contains(body, created.Token) ||
		strings.Contains(body, strings.Split(created.Token, ".")[2]) {
		t.Fatalf("pending list = %s", body)
	}

	// The worker redeems the token (the gRPC Enroll path, called directly).
	tok, err := enrolltoken.Parse(created.Token)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Deps.Workers.Enroll(context.Background(), workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: workerCSR(t), Version: "v0.2c"})
	if err != nil {
		t.Fatal(err)
	}
	type workerList struct {
		Workers []struct {
			ID            string  `json:"id"`
			Status        string  `json:"status"`
			ZoneSlug      string  `json:"zone_slug"`
			CertExpiresAt *string `json:"cert_expires_at"`
		} `json:"workers"`
	}
	var list workerList
	decodeInto(t, requestJSON(t, c, http.MethodGet, base+"/workers", nil), &list)
	if len(list.Workers) != 1 || list.Workers[0].Status != "offline" || list.Workers[0].ZoneSlug != "default" || list.Workers[0].CertExpiresAt == nil {
		t.Fatalf("workers = %+v", list)
	}
	// The redeemed enrollment is no longer pending.
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/worker-enrollments", nil)); strings.Contains(body, created.Enrollment.ID) {
		t.Fatalf("a redeemed enrollment must leave the pending list: %s", body)
	}
	id := res.Worker.ID.String()
	if resp := requestJSON(t, c, http.MethodDelete, base+"/workers/"+id, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete an active worker: %d", resp.StatusCode)
	}
	if resp := postJSON(t, oc, base+"/workers/"+id+"/revoke", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator revoke: %d", resp.StatusCode)
	}
	resp = postJSON(t, c, base+"/workers/"+id+"/revoke", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	var revoked struct {
		Worker struct {
			Status   string `json:"status"`
			ZoneSlug string `json:"zone_slug"`
		} `json:"worker"`
		StreamsClosed *int `json:"streams_closed"`
		JobsRequeued  *int `json:"jobs_requeued"`
		JobsEnded     *int `json:"jobs_ended"`
	}
	decodeInto(t, resp, &revoked)
	// 2.65: the revoke response's worker object used to leave zone_slug
	// empty (View{Worker: w} with no ZoneSlug set) even though the schema
	// requires one, same as every other worker DTO.
	if revoked.Worker.Status != "revoked" || revoked.Worker.ZoneSlug != "default" || revoked.StreamsClosed == nil || revoked.JobsRequeued == nil || revoked.JobsEnded == nil {
		t.Fatalf("revoke response = %+v", revoked)
	}
	leaf, _ := pem.Decode(res.CertPEM)
	crt, _ := x509.ParseCertificate(leaf.Bytes)
	if _, err := s.Deps.Workers.GetBySerial(context.Background(), crt.SerialNumber); err == nil {
		t.Fatal("a revoked worker's certificate must be refused")
	}
	list = workerList{}
	decodeInto(t, requestJSON(t, c, http.MethodGet, base+"/workers", nil), &list)
	if len(list.Workers) != 1 || list.Workers[0].Status != "revoked" {
		t.Fatalf("workers after revoke = %+v", list)
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/workers/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, base+"/workers/"+id+"/revoke", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke a deleted worker: %d", resp.StatusCode)
	}
	// Withdraw a pending enrollment.
	var second struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
		Token string `json:"token"`
	}
	decodeInto(t, postJSON(t, c, base+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "hel-2"}), &second)
	if resp := requestJSON(t, c, http.MethodDelete, base+"/worker-enrollments/"+second.Enrollment.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/worker-enrollments/"+uuid.NewString(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("withdraw unknown: %d", resp.StatusCode)
	}
	body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/activity?limit=100", nil))
	for _, action := range []string{"POST createWorkerEnrollment", "POST revokeWorker", "DELETE deleteWorker", "DELETE withdrawWorkerEnrollment"} {
		if !strings.Contains(body, `"`+action+`"`) {
			t.Fatalf("activity lacks %q", action)
		}
	}
	// Tokens are shown once: never in the activity feed, not even in part.
	for _, token := range []string{created.Token, second.Token} {
		secret := strings.Split(token, ".")[2] // pxw1.<id>.<secret>.<ca pin>
		if strings.Contains(body, token) || strings.Contains(body, secret) {
			t.Fatalf("the activity feed leaks an enrollment token: %s", body)
		}
	}
}

// failingRequeuer always refuses, so Revoke's own store write (which
// commits before this hook even runs) can be told apart from the
// requeue step that follows it.
type failingRequeuer struct{}

func (failingRequeuer) RequeueWorkerJobs(context.Context, uuid.UUID, uuid.UUID, string) (int, int, error) {
	return 0, 0, errors.New("requeue boom")
}

// TestRevokeWorkerAuditsAFailedRequeue (2.65): Revoke's store write commits
// the revocation before the job-requeue step that follows it; when that
// step fails, the handler answers 500 — but the revoke itself already
// happened, and auditMiddleware records nothing for a non-2xx response (see
// its own comment), so without the handler's own direct record that real
// mutation would leave no trace at all beyond an ERROR log line.
func TestRevokeWorkerAuditsAFailedRequeue(t *testing.T) {
	s := apitest.New(t, func(s *apitest.Server) {
		s.Deps.Workers.SetRevokeHooks(nil, failingRequeuer{})
	})
	owner := s.SeedUser("workers-revoke-fail@example.test", "correct horse battery")
	org := s.SeedOrg("workers-revoke-fail", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/workers-revoke-fail"
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	w, err := s.Deps.Workers.Register(context.Background(), org, workers.RegisterInput{ZoneID: zones[0].ID, Name: "fra-1", CertSerial: big.NewInt(9001)})
	if err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, c, base+"/workers/"+w.ID.String()+"/revoke", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("revoke with a failing requeue hook: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	list, err := s.Deps.Workers.List(context.Background(), org)
	if err != nil || len(list) != 1 || list[0].Status != workers.StatusRevoked {
		t.Fatalf("the worker must still be revoked despite the requeue failure: %+v, %v", list, err)
	}

	body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/activity?limit=100", nil))
	if !strings.Contains(body, `"POST revokeWorker"`) || !strings.Contains(body, "requeue") {
		t.Fatalf("activity feed lacks the failed-requeue revoke event: %s", body)
	}
}

// TestRevokeUnknownWorkerWritesNoAuditEvent is the companion review finding
// to TestRevokeWorkerAuditsAFailedRequeue: Revoke's own store write (not
// the requeue step after it) can also fail — unknown id, wrong
// organization, an RLS or database error — in which case it returns a
// zero Worker{} and nothing was ever revoked. Recording an event
// unconditionally on any error from Revoke would write "revoked worker "
// (an empty name) into the activity feed for a worker nobody touched,
// which is worse than the gap the direct-record call exists to close.
func TestRevokeUnknownWorkerWritesNoAuditEvent(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-revoke-404@example.test", "correct horse battery")
	s.SeedOrg("workers-revoke-404", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/workers-revoke-404"

	resp := postJSON(t, c, base+"/workers/"+uuid.NewString()+"/revoke", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke of an unknown worker: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/activity?limit=100", nil))
	if strings.Contains(body, `"POST revokeWorker"`) {
		t.Fatalf("no worker was ever revoked; the activity feed must not record one: %s", body)
	}
}

func TestWorkerEnrollmentRefusals(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-refuse@example.test", "correct horse battery")
	org := s.SeedOrg("workers-refuse", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/workers-refuse"
	zones, _ := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	// Each case pins its own status AND what the answer says. The two
	// refusals are different in kind — the service's own rules answer 400
	// with a reason an operator can act on, the request schema answers 422
	// before any handler runs — and a test that accepted either for every
	// case could not tell them apart: a service that stopped refusing the
	// reserved name would still pass as long as something, anywhere,
	// answered 422.
	for name, tc := range map[string]struct {
		body map[string]any
		want int
		says string
	}{
		"reserved name": {map[string]any{"zone_id": zones[0].ID.String(), "name": workers.EmbeddedName}, http.StatusBadRequest, "is reserved for the manager"},
		"unknown zone":  {map[string]any{"zone_id": uuid.NewString(), "name": "x-1"}, http.StatusBadRequest, "unknown zone"},
		"bad zone id":   {map[string]any{"zone_id": "nope", "name": "x-1"}, http.StatusUnprocessableEntity, "body.zone_id"},
		"empty name":    {map[string]any{"zone_id": zones[0].ID.String(), "name": ""}, http.StatusUnprocessableEntity, "body.name"},
	} {
		resp := postJSON(t, c, base+"/worker-enrollments", tc.body)
		body := mustReadBody(t, resp)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: %d, want %d: %s", name, resp.StatusCode, tc.want, body)
		}
		if !strings.Contains(body, tc.says) {
			t.Fatalf("%s: answer must name %q: %s", name, tc.says, body)
		}
	}
	if resp := postJSON(t, c, base+"/workers/not-a-uuid/revoke", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad worker id: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, c, http.MethodDelete, base+"/workers/"+uuid.NewString(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown worker: %d", resp.StatusCode)
	}
}

func TestWorkerEnrollmentNeedsTheWorkerEndpoint(t *testing.T) {
	s := apitest.New(t, func(s *apitest.Server) { s.Deps.WorkerEndpoint = api.WorkerEndpoint{} })
	owner := s.SeedUser("workers-off@example.test", "correct horse battery")
	org := s.SeedOrg("workers-off", owner)
	zones, _ := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/workers-off/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "x"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(mustReadBody(t, resp), "PULSIX_GRPC_LISTEN") {
		t.Fatalf("disabled endpoint: %d", resp.StatusCode)
	}
}

// TestWorkerRoutesCrossOrgIsolation: every worker route is scoped to the
// organization in its path, not to the id alone. One user owns both
// organizations here on purpose — a 404 from a non-member would prove
// nothing, since membership alone would already have refused the call.
// An id from the first organization used under the second must read as
// absent on every route that takes one, and neither list may mention the
// other organization's rows.
func TestWorkerRoutesCrossOrgIsolation(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-xorg@example.test", "correct horse battery")
	orgA := s.SeedOrg("workers-xorg-a", owner)
	s.SeedOrg("workers-xorg-b", owner)
	c := s.LoginAs(owner)
	a := s.HTTP.URL + "/api/v1/orgs/workers-xorg-a"
	b := s.HTTP.URL + "/api/v1/orgs/workers-xorg-b"
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), orgA), orgA)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}

	// A redeemed worker and a still-pending enrollment, both in org A.
	var created struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
		Token string `json:"token"`
	}
	decodeInto(t, postJSON(t, c, a+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "xorg-1"}), &created)
	tok, err := enrolltoken.Parse(created.Token)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Deps.Workers.Enroll(context.Background(), workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: workerCSR(t), Version: "v0.2c"})
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
	}
	decodeInto(t, postJSON(t, c, a+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "xorg-2"}), &pending)

	workerID, enrollmentID := res.Worker.ID.String(), pending.Enrollment.ID
	for name, call := range map[string]struct {
		method, url string
	}{
		"revoke":            {http.MethodPost, b + "/workers/" + workerID + "/revoke"},
		"delete worker":     {http.MethodDelete, b + "/workers/" + workerID},
		"delete enrollment": {http.MethodDelete, b + "/worker-enrollments/" + enrollmentID},
	} {
		if resp := requestJSON(t, c, call.method, call.url, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s across organizations: %d %s", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, b+"/workers", nil)); strings.Contains(body, workerID) {
		t.Fatalf("org B lists org A's worker: %s", body)
	}
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, b+"/worker-enrollments", nil)); strings.Contains(body, enrollmentID) {
		t.Fatalf("org B lists org A's pending enrollment: %s", body)
	}
	// The refusals changed nothing: org A still has both rows.
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, a+"/workers", nil)); !strings.Contains(body, workerID) || !strings.Contains(body, `"status":"offline"`) {
		t.Fatalf("org A's worker must be untouched: %s", body)
	}
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, a+"/worker-enrollments", nil)); !strings.Contains(body, enrollmentID) {
		t.Fatalf("org A's enrollment must be untouched: %s", body)
	}
}

// TestWorkerEnrollmentCreateRefusesBearerToken is backlog 7.49: a worker
// enrollment token mints an identity (a worker with a client certificate)
// that outlives whatever created it, so a pxt_ API token must not mint one
// — revoking a leaked API token would leave the worker it enrolled
// untouched. Listing and withdrawing stay open to the token, and an
// MFA-complete admin session still creates.
func TestWorkerEnrollmentCreateRefusesBearerToken(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-bearer-owner@example.test", "correct horse battery")
	admin := s.SeedUser("workers-bearer-admin@example.test", "correct horse battery")
	org := s.SeedOrg("workers-bearer", owner)
	s.AddMember(org, admin, tenancy.RoleAdmin)
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	plain, _, err := s.Deps.Tokens.Create(context.Background(), admin, org, "ci", "admin", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := "/api/v1/orgs/workers-bearer/worker-enrollments"

	resp := bearerRequestJSON(t, s, http.MethodPost, path, plain, map[string]any{"zone_id": zones[0].ID.String(), "name": "bearer-1"})
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "interactive session required") {
		t.Fatalf("create with a bearer token: status %d, want 403 interactive session required: %s", resp.StatusCode, body)
	}
	// Nothing was minted.
	if body := mustReadBody(t, requestJSON(t, s.LoginAs(owner), http.MethodGet, s.HTTP.URL+path, nil)); strings.Contains(body, "bearer-1") {
		t.Fatalf("a refused create minted an enrollment: %s", body)
	}

	// An MFA-complete admin session still creates it.
	resp = postJSON(t, s.LoginAs(admin), s.HTTP.URL+path, map[string]any{"zone_id": zones[0].ID.String(), "name": "session-1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with an admin session: %d: %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created struct {
		Enrollment struct {
			ID string `json:"id"`
		} `json:"enrollment"`
	}
	decodeInto(t, resp, &created)

	// The bearer token still lists and withdraws.
	resp = bearerRequest(t, s, http.MethodGet, path, plain)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, created.Enrollment.ID) {
		t.Fatalf("list with a bearer token: status %d: %s", resp.StatusCode, body)
	}
	resp = bearerRequest(t, s, http.MethodDelete, path+"/"+created.Enrollment.ID, plain)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("withdraw with a bearer token: status %d: %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// TestWorkerVersionStatus is backlog 7.3: each worker row says how its
// build relates to the manager's, so the Workers page can badge the ones to
// update. The verdict is shown, never enforced.
func TestWorkerVersionStatus(t *testing.T) {
	s := apitest.New(t, apitest.WithVersion("v1.4.0"))
	owner := s.SeedUser("worker-skew-owner@example.test", "correct horse battery")
	org := s.SeedOrg("worker-skew", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/worker-skew"
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}

	want := map[string]string{
		"w-current": "current", "w-previous": "previous_minor", "w-old": "update_required",
		"w-newer": "newer", "w-dev": "unknown", "w-none": "unknown",
	}
	versions := map[string]string{
		"w-current": "v1.4.7", "w-previous": "v1.3.0", "w-old": "v1.1.0",
		"w-newer": "v1.5.0", "w-dev": "dev", "w-none": "",
	}
	for name, version := range versions {
		var created struct {
			Token string `json:"token"`
		}
		decodeInto(t, postJSON(t, c, base+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": name}), &created)
		tok, err := enrolltoken.Parse(created.Token)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Deps.Workers.Enroll(context.Background(), workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: workerCSR(t), Version: version}); err != nil {
			t.Fatal(err)
		}
	}

	var list struct {
		Workers []struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			VersionStatus string `json:"version_status"`
		} `json:"workers"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, base+"/workers", nil), &list)
	if len(list.Workers) != len(want) {
		t.Fatalf("workers = %+v", list.Workers)
	}
	for _, w := range list.Workers {
		if w.VersionStatus != want[w.Name] {
			t.Errorf("worker %s (version %q): version_status = %q, want %q", w.Name, w.Version, w.VersionStatus, want[w.Name])
		}
	}
}

// A manager without a version (a development build) has no basis for a
// verdict: every worker reads "unknown", never "update_required".
func TestWorkerVersionStatusUnknownForDevelopmentManager(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("worker-dev-owner@example.test", "correct horse battery")
	org := s.SeedOrg("worker-dev", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/worker-dev"
	zones, err := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	var created struct {
		Token string `json:"token"`
	}
	decodeInto(t, postJSON(t, c, base+"/worker-enrollments", map[string]any{"zone_id": zones[0].ID.String(), "name": "w-1"}), &created)
	tok, err := enrolltoken.Parse(created.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deps.Workers.Enroll(context.Background(), workers.EnrollInput{TokenID: tok.ID, Secret: tok.Secret, CSRPEM: workerCSR(t), Version: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if body := mustReadBody(t, requestJSON(t, c, http.MethodGet, base+"/workers", nil)); !strings.Contains(body, `"version_status":"unknown"`) {
		t.Fatalf("workers = %s", body)
	}
}
