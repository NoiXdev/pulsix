package hosts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// CreateHostWith inserts h, then x's tags and jump host, in one
// transaction bound to h's organization.
func (s *SQLStore) CreateHostWith(ctx context.Context, h Host, x HostExtras) (Host, error) {
	facts, err := marshalFacts(h.Facts)
	if err != nil {
		return Host{}, err
	}
	var out Host
	err = s.db.WithOrg(ctx, h.OrgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.CreateHost(ctx, gen.CreateHostParams{
			ID: h.ID, OrgID: h.OrgID, ZoneID: h.ZoneID, Name: h.Name, Address: h.Address,
			Managed: h.Managed, Monitored: h.Monitored, Facts: facts,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return ErrNameTaken
			}
			return fmt.Errorf("create host: %w", err)
		}
		if out, err = fromHostRow(row); err != nil {
			return err
		}
		return writeExtras(ctx, q, h.OrgID, h.ID, x)
	})
	if err != nil {
		return Host{}, err
	}
	return out, nil
}

// UpdateHostWith updates h's row under its revision, then writes x's tags
// and jump host, in one transaction bound to h's organization. A stale
// revision or a missing host fails before x is touched.
func (s *SQLStore) UpdateHostWith(ctx context.Context, h Host, x HostExtras) (Host, error) {
	var out Host
	err := s.db.WithOrg(ctx, h.OrgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.UpdateHost(ctx, gen.UpdateHostParams{
			ID: h.ID, OrgID: h.OrgID, ZoneID: h.ZoneID, Name: h.Name, Address: h.Address,
			Managed: h.Managed, Monitored: h.Monitored, Revision: h.Revision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// No row means one of two different answers, and the caller
			// needs them apart: the host is gone (404), or someone saved
			// first and this view is stale (412). Only a read can tell.
			if _, getErr := q.GetHost(ctx, gen.GetHostParams{OrgID: h.OrgID, ID: h.ID}); getErr != nil {
				return mapErr(getErr)
			}
			return ErrRevisionMismatch
		}
		if err != nil {
			if isUniqueViolation(err) {
				return ErrNameTaken
			}
			return mapErr(err)
		}
		if out, err = fromHostRow(row); err != nil {
			return err
		}
		return writeExtras(ctx, q, h.OrgID, h.ID, x)
	})
	if err != nil {
		return Host{}, err
	}
	return out, nil
}

// writeExtras writes x's manual tags and jump host for the host, on q,
// which is bound to the caller's transaction.
func writeExtras(ctx context.Context, q *gen.Queries, orgID, hostID uuid.UUID, x HostExtras) error {
	if x.Tags != nil {
		if err := q.DeleteManualHostTags(ctx, gen.DeleteManualHostTagsParams{OrgID: orgID, HostID: hostID}); err != nil {
			return fmt.Errorf("clear manual tags: %w", err)
		}
		for _, t := range *x.Tags {
			if err := q.InsertHostTag(ctx, gen.InsertHostTagParams{
				HostID: hostID, OrgID: orgID, Key: t.Key, Value: t.Value, Source: string(t.Source),
			}); err != nil {
				return fmt.Errorf("insert tag %q: %w", t.Key, err)
			}
		}
	}
	if x.Jump != nil {
		err := q.UpsertHostBastion(ctx, gen.UpsertHostBastionParams{
			HostID: hostID, OrgID: orgID, BastionMode: x.Jump.Mode, BastionID: db.NullUUID(x.Jump.BastionID),
		})
		// The host row is written in this same transaction, so the only
		// foreign key this statement can miss is the jump host's: one that
		// does not exist, or (the composite key) belongs to another org.
		if db.IsForeignKeyViolation(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("set jump host: %w", err)
		}
	}
	return nil
}
