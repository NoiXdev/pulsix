package checks

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// CurrentLabel returns one tuple's current discrete label, read on tx —
// the alerts executor's savepoint, for the alert detail (backlog 3.13b).
// "" when the tuple has no current row, or its current value is a number
// or an error: none of those is a label to report. tx is the caller's and
// may be unscoped (the evaluator's is), so the statement names org_id
// itself.
func (s *SQLStore) CurrentLabel(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, checkKey, item, metric string) (string, error) {
	label, err := gen.New(tx).GetCheckCurrentLabel(ctx, gen.GetCheckCurrentLabelParams{
		OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item, Metric: metric,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("current label of %s/%q %s: %w", checkKey, item, metric, err)
	}
	return errString(label), nil
}
