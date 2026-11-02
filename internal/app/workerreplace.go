package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/workers"
)

// workerReplaceRegistry is what `worker add --replace` needs of the worker
// registry (workers.Service implements it).
type workerReplaceRegistry interface {
	List(ctx context.Context, orgID uuid.UUID) ([]workers.Worker, error)
	Revoke(ctx context.Context, orgID, id uuid.UUID) (workers.Worker, workers.RevokeResult, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// replacingRegister is the registrar of `worker add --replace`: when the
// name is taken in the organization — a restored database on a new server
// still knows the worker the compose stack enrolls — the existing worker is
// revoked and deleted and the new certificate registered under its name.
//
// A revoked worker is left alone: replacing it would undo the revocation.
// The worker's jobs are not requeued here; the manager's reaper requeues
// whatever no worker is attached to.
type replacingRegister struct {
	reg      workerRegistrar
	registry workerReplaceRegistry
	stderr   io.Writer
}

// Register implements workerRegistrar.
func (r replacingRegister) Register(ctx context.Context, orgID uuid.UUID, name string, zoneID uuid.UUID, serial *big.Int) error {
	err := r.reg.Register(ctx, orgID, name, zoneID, serial)
	if !errors.Is(err, workers.ErrNameTaken) {
		return err
	}
	list, lerr := r.registry.List(ctx, orgID)
	if lerr != nil {
		return fmt.Errorf("find the worker to replace: %w", lerr)
	}
	var old *workers.Worker
	for i := range list {
		if list[i].Name == name {
			old = &list[i]
			break
		}
	}
	switch {
	case old == nil:
		return err
	case old.Status == workers.StatusRevoked:
		return fmt.Errorf("the worker %q was revoked, and --replace does not enroll a revoked worker again: "+
			"delete it on the Workers page first", name)
	}
	if _, _, err := r.registry.Revoke(ctx, orgID, old.ID); err != nil {
		return fmt.Errorf("revoke the worker %q being replaced: %w", name, err)
	}
	if err := r.registry.Delete(ctx, orgID, old.ID); err != nil {
		return fmt.Errorf("delete the worker %q being replaced: %w", name, err)
	}
	_, _ = fmt.Fprintf(r.stderr, "worker add: revoked and deleted the existing worker %q to enroll it again\n", name)
	return r.reg.Register(ctx, orgID, name, zoneID, serial)
}

// Compile-time proof that the registry fits the port.
var _ workerReplaceRegistry = (*workers.Service)(nil)
