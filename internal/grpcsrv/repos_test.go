// ListRepositories (backlog 7.95): the worker's own organization and its
// repositories, never another organization's.
package grpcsrv

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// TestListRepositoriesAnswersForTheWorkersOrganization: the answer names
// the organization the worker's registry row belongs to and that
// organization's repositories; another organization's stay out.
func TestListRepositoriesAnswersForTheWorkersOrganization(t *testing.T) {
	h := newHarness(t)
	mine := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	other := uuid.Must(uuid.NewV7())
	h.repos.byOrg[orgID] = mine
	h.repos.byOrg[other] = []uuid.UUID{uuid.Must(uuid.NewV7())}
	certPEM, keyPEM := h.worker()

	resp, err := h.client(certPEM, keyPEM).ListRepositories(t.Context(), &workerpb.ListRepositoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOrgId() != orgID.String() {
		t.Errorf("org_id = %q, want %s", resp.GetOrgId(), orgID)
	}
	want := []string{mine[0].String(), mine[1].String()}
	if !slices.Equal(resp.GetRepoIds(), want) {
		t.Errorf("repo_ids = %v, want %v", resp.GetRepoIds(), want)
	}
	if !slices.Equal(h.repos.asked, []uuid.UUID{orgID}) {
		t.Errorf("asked about %v, want only the worker's organization", h.repos.asked)
	}
}

// TestListRepositoriesEmpty: an organization without repositories gets
// an empty list, which is an answer, not an error.
func TestListRepositoriesEmpty(t *testing.T) {
	h := newHarness(t)
	certPEM, keyPEM := h.worker()
	resp, err := h.client(certPEM, keyPEM).ListRepositories(t.Context(), &workerpb.ListRepositoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetOrgId() != orgID.String() || len(resp.GetRepoIds()) != 0 {
		t.Errorf("response = %v, want the organization and no repositories", resp)
	}
}

// TestListRepositoriesFailure: a failed lookup is Internal, so the worker
// removes nothing.
func TestListRepositoriesFailure(t *testing.T) {
	h := newHarness(t)
	h.repos.err = errors.New("db down")
	certPEM, keyPEM := h.worker()
	_, err := h.client(certPEM, keyPEM).ListRepositories(t.Context(), &workerpb.ListRepositoriesRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %s (%v), want Internal", status.Code(err), err)
	}
}

// TestListRepositoriesUnwired: a server built without a lister answers
// Unimplemented, the answer an older manager gives.
func TestListRepositoriesUnwired(t *testing.T) {
	h := newHarness(t)
	h.srv.deps.Repos = nil
	certPEM, keyPEM := h.worker()
	_, err := h.client(certPEM, keyPEM).ListRepositories(t.Context(), &workerpb.ListRepositoriesRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %s (%v), want Unimplemented", status.Code(err), err)
	}
}

// TestListRepositoriesNeedsACertificate: without a client certificate the
// interceptor refuses the call before the lister is asked.
func TestListRepositoriesNeedsACertificate(t *testing.T) {
	h := newHarness(t)
	_, err := h.client(nil, nil).ListRepositories(t.Context(), &workerpb.ListRepositoriesRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %s (%v), want Unauthenticated", status.Code(err), err)
	}
	if len(h.repos.asked) != 0 {
		t.Errorf("the lister was asked about %v", h.repos.asked)
	}
}
