package probe

import (
	"context"

	"github.com/google/uuid"
)

type orgKey struct{}

// WithOrg returns a context that says the probe runs for organization org,
// so shared resources inside a probe (the http probe's JSON stage gate) can
// share fairly between organizations. A context without one belongs to the
// nil organization.
func WithOrg(ctx context.Context, org uuid.UUID) context.Context {
	return context.WithValue(ctx, orgKey{}, org)
}

// OrgFrom returns the organization WithOrg set, or the nil UUID.
func OrgFrom(ctx context.Context) uuid.UUID {
	org, _ := ctx.Value(orgKey{}).(uuid.UUID)
	return org
}
