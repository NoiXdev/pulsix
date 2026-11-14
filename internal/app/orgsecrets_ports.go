package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/orgsecrets"
)

// definitionLister is the part of checks.Service the secret store's
// "used by" reads.
type definitionLister interface {
	ListDefinitions(ctx context.Context, orgID uuid.UUID) ([]checks.Definition, error)
}

// secretCheckRefs adapts the checks service to orgsecrets.CheckRefs: every
// check definition of the organization — its own, a monitoring group's, a
// host's and the projections of shared definitions, which are rows of the
// same table — with its key, plugin and params. Which of them use a
// secret is the orgsecrets service's call (server-run plugins only).
type secretCheckRefs struct{ l definitionLister }

// NewSecretCheckRefs wires the adapter (also used by internal/api/apitest).
func NewSecretCheckRefs(l definitionLister) orgsecrets.CheckRefs { return secretCheckRefs{l: l} }

// CheckRefs implements orgsecrets.CheckRefs.
func (r secretCheckRefs) CheckRefs(ctx context.Context, orgID uuid.UUID) ([]orgsecrets.CheckRef, error) {
	defs, err := r.l.ListDefinitions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list check definitions: %w", err)
	}
	out := make([]orgsecrets.CheckRef, len(defs))
	for i, d := range defs {
		out[i] = orgsecrets.CheckRef{Key: d.Key, Plugin: d.Plugin, Params: d.Params}
	}
	return out, nil
}
