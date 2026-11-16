package templates

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// followUpGraphLockKey namespaces the follow-up graph's advisory lock key
// within hashtext's output, apart from the other advisory locks keyed on an
// organization id (sshconf's chain lock has its own prefix).
const followUpGraphLockKey = "template-follow-ups:"

// LockFollowUpGraph implements FollowUpStore: a transaction-scoped advisory
// lock keyed on the organization, held until the surrounding InTx commits
// or rolls back. Two saves that would jointly close a cycle serialize here,
// and the second validates against the first's committed edges (the same
// pattern as sshconf's WithChainLock).
func (s *SQLStore) LockFollowUpGraph(ctx context.Context, orgID uuid.UUID) error {
	if _, err := s.d.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, followUpGraphLockKey+orgID.String()); err != nil {
		return fmt.Errorf("acquire follow-up graph lock: %w", err)
	}
	return nil
}

// ReplaceFollowUps implements FollowUpStore: delete then insert in one
// transaction (a savepoint when the caller is inside InTx), so a failing
// insert never leaves the template with none.
func (s *SQLStore) ReplaceFollowUps(ctx context.Context, orgID, templateID uuid.UUID, ids []uuid.UUID) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace template follow-ups: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	if err := q.DeleteTemplateFollowUps(ctx, gen.DeleteTemplateFollowUpsParams{OrgID: orgID, TemplateID: templateID}); err != nil {
		return fmt.Errorf("delete template follow-ups: %w", err)
	}
	for i, id := range ids {
		if err := q.InsertTemplateFollowUp(ctx, gen.InsertTemplateFollowUpParams{
			OrgID: orgID, TemplateID: templateID, FollowUpTemplateID: id, Position: int32(i),
		}); err != nil {
			return mapWriteErr(err, "insert template follow-up")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace template follow-ups: %w", err)
	}
	return nil
}

// ListFollowUps implements FollowUpStore.
func (s *SQLStore) ListFollowUps(ctx context.Context, orgID, templateID uuid.UUID) ([]FollowUp, error) {
	rows, err := s.q.ListTemplateFollowUps(ctx, gen.ListTemplateFollowUpsParams{OrgID: orgID, TemplateID: templateID})
	if err != nil {
		return nil, fmt.Errorf("list template follow-ups: %w", err)
	}
	out := make([]FollowUp, len(rows))
	for i, r := range rows {
		out[i] = FollowUp{TemplateID: r.FollowUpTemplateID, Name: r.FollowUpName}
	}
	return out, nil
}

// ListOrgFollowUps implements FollowUpStore in one query.
func (s *SQLStore) ListOrgFollowUps(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]FollowUp, error) {
	rows, err := s.q.ListOrgTemplateFollowUps(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list org template follow-ups: %w", err)
	}
	out := map[uuid.UUID][]FollowUp{}
	for _, r := range rows {
		out[r.TemplateID] = append(out[r.TemplateID], FollowUp{TemplateID: r.FollowUpTemplateID, Name: r.FollowUpName})
	}
	return out, nil
}

// FollowUpReferrers implements FollowUpStore.
func (s *SQLStore) FollowUpReferrers(ctx context.Context, orgID, templateID uuid.UUID) ([]string, error) {
	names, err := s.q.ListFollowUpReferrers(ctx, gen.ListFollowUpReferrersParams{OrgID: orgID, FollowUpTemplateID: templateID})
	if err != nil {
		return nil, fmt.Errorf("list follow-up referrers: %w", err)
	}
	return names, nil
}
