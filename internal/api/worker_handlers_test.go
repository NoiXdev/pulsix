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
	}
	decodeInto(t, resp, &created)
	if !strings.HasPrefix(created.Token, "pxw1.") || !strings.Contains(created.DockerSnippet, created.Token) ||
		!strings.Contains(created.DockerSnippet, "PULSIX_MANAGER_ADDR=localhost:8444") || !strings.Contains(created.ComposeSnippet, "PULSIX_ENROLL_TOKEN") {
		t.Fatalf("created = %+v", created)
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

func TestWorkerEnrollmentRefusals(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("workers-refuse@example.test", "correct horse battery")
	org := s.SeedOrg("workers-refuse", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/workers-refuse"
	zones, _ := s.Deps.Hosts.ListZones(db.WithOrgScope(context.Background(), org), org)
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"reserved name": {map[string]any{"zone_id": zones[0].ID.String(), "name": workers.EmbeddedName}, http.StatusBadRequest},
		"unknown zone":  {map[string]any{"zone_id": uuid.NewString(), "name": "x-1"}, http.StatusBadRequest},
		"bad zone id":   {map[string]any{"zone_id": "nope", "name": "x-1"}, http.StatusBadRequest},
	} {
		if resp := postJSON(t, c, base+"/worker-enrollments", tc.body); resp.StatusCode != tc.want && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, mustReadBody(t, resp))
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
