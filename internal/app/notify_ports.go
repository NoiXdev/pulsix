package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/notify"
)

// openAlerts is the alert store's one read notify's sender needs.
type openAlerts interface {
	OpenAlertIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
}

// notifyAlertStates is notify.AlertStates over the alert store (backlog
// 4.8 review I1). The dispatcher sends outside any organization's scope,
// so the read is bound to the delivery's organization here.
type notifyAlertStates struct{ store openAlerts }

// NewNotifyAlertStates adapts the alert store for the notification
// sender; apitest wires it the way Build does.
func NewNotifyAlertStates(store openAlerts) notify.AlertStates {
	return notifyAlertStates{store: store}
}

// ClosedAlerts reports every id that is not an open alert of orgID now:
// closed, or gone with its host.
func (a notifyAlertStates) ClosedAlerts(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	open, err := a.store.OpenAlertIDs(db.WithOrgScope(ctx, orgID), orgID, ids)
	if err != nil {
		return nil, err
	}
	isOpen := make(map[uuid.UUID]bool, len(open))
	for _, id := range open {
		isOpen[id] = true
	}
	closed := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if !isOpen[id] {
			closed[id] = true
		}
	}
	return closed, nil
}
