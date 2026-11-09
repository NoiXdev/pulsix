// ListRepositories (backlog 7.95): the authenticated worker's organization
// and the ids of its repositories, so the worker can remove the caches of
// every other one. The organization comes from the registry row the auth
// interceptor resolved, never from the request.
package grpcsrv

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// ListRepositories implements workerpb.WorkerServiceServer.ListRepositories.
func (s *Server) ListRepositories(ctx context.Context, _ *workerpb.ListRepositoriesRequest) (*workerpb.ListRepositoriesResponse, error) {
	_, worker, err := authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Repos == nil {
		return nil, status.Error(codes.Unimplemented, "this manager does not list repositories")
	}
	ids, err := s.deps.Repos.RepoIDs(ctx, worker.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list repositories: %v", err)
	}
	resp := &workerpb.ListRepositoriesResponse{OrgId: worker.OrgID.String(), RepoIds: make([]string, len(ids))}
	for i, id := range ids {
		resp.RepoIds[i] = id.String()
	}
	return resp, nil
}
