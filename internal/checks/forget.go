package checks

// Forgetting items that are gone (backlog 3.12b, spec
// docs/superpowers/specs/2026-11-12-forget-items-design.md). A check's item
// — a container, mount, interface or unit — that disappears for good keeps
// its last values in check_current for ever (Current's own doc comment),
// and those rows count against MaxTuplesPerHost. Forgetting the item
// removes, for every metric of it, its current values, its rule states and
// their state-change history, and closes every open alert on it with the
// reason forgotten. Samples (graphs) and item decisions are untouched. An
// item that reports again afterwards simply appears again, like a new one.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AlertCloser closes every open alert on one item inside the forget's own
// transaction. internal/alerts.Service satisfies it (CloseForgotten), so
// the alert events and notification routing stay in that package; this
// package depends on the port only. tx is the store's transaction, handed
// through the service untouched.
type AlertCloser interface {
	CloseForgotten(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, checkKey, item string, now time.Time) (int, error)
}

// WithAlertCloser wires the alert-closing port ForgetItem and
// ForgetExpired need. A Service built without it refuses to forget rather
// than leave an open alert on an item that no longer exists.
func WithAlertCloser(c AlertCloser) Option { return func(s *Service) { s.alerts = c } }

// ItemRef names one item of one host: every metric (check_key, item, *).
type ItemRef struct {
	OrgID, HostID  uuid.UUID
	CheckKey, Item string
}

// ItemNewest is what the store reads about an item inside its forget
// transaction, after the host's evaluation lock: how many current rows the
// item has, its newest reading (over all of its metrics) and the host's
// newest reading (over all of its items). Item and Host are meaningless
// when Rows is 0.
type ItemNewest struct {
	Rows       int
	Item, Host time.Time
}

// ForgetSteps are the service's two say-sos inside the store's forget
// transaction. Proceed (optional) runs first, after the host's evaluation
// lock is held, and false abandons the forget with nothing changed. Within
// runs last, after the deletes, on the same transaction — the alert close.
// An error from it rolls the whole forget back.
type ForgetSteps struct {
	Proceed func(ItemNewest) bool
	Within  func(tx pgx.Tx) error
}

// itemExpired is the automatic expiry's rule (ListExpiredItems is the same
// rule in SQL, for the selection): the item's newest reading is older than
// now - period, and its host has a reading newer than that plus the
// period — the host kept reporting for the whole period without the item.
// A host that stopped reporting altogether never satisfies the second
// half, so an offline host keeps every value. Both comparisons are strict.
func itemExpired(n ItemNewest, now time.Time, period time.Duration) bool {
	if n.Rows == 0 {
		return false
	}
	return n.Item.Before(now.Add(-period)) && n.Host.After(n.Item.Add(period))
}

// errNoAlertCloser is what ForgetItem and ForgetExpired return on a
// Service built without WithAlertCloser: a wiring mistake, refused before
// the store is reached.
var errNoAlertCloser = errors.New("checks: forgetting an item needs an alert closer (WithAlertCloser)")

// ForgetItem forgets one item of hostID at once (the host page's Forget
// action): its current values, rule states and state-change history for
// every metric, and every open alert on it, closed as forgotten — all in
// one transaction. ErrNotFound means the item has no current value to
// forget (never reported, already forgotten, or every row of it being
// written by an ingest at this very moment, which then lands after the
// forget anyway). item may be "", the whole-host item.
func (s *Service) ForgetItem(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item string) error {
	if s.alerts == nil {
		return errNoAlertCloser
	}
	if checkKey == "" {
		return fmt.Errorf("%w: check_key is required", ErrInvalidInput)
	}
	ref := ItemRef{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item}
	forgot, err := s.store.ForgetItem(ctx, ref, ForgetSteps{Within: s.closeAlerts(ctx, ref, s.now())})
	if err != nil {
		return fmt.Errorf("forget item: %w", err)
	}
	if !forgot {
		return ErrNotFound
	}
	return nil
}

// ForgetExpired forgets up to limit items, across every organization, that
// have delivered no reading for period while their host kept reporting
// (itemExpired). Each item is forgotten in a transaction of its own, and
// the rule is checked again inside it, after the host's evaluation lock: an
// item that reported again between the selection and its forget is left
// alone. It returns how many items it forgot per organization.
//
// An item whose forget fails does not stop the others — otherwise one
// broken item, always the oldest, would starve the rest round after round;
// the error returned then names how many failed and the first failure,
// next to the counts of what did succeed.
func (s *Service) ForgetExpired(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error) {
	if s.alerts == nil {
		return nil, errNoAlertCloser
	}
	if period <= 0 || limit <= 0 {
		return nil, fmt.Errorf("%w: period and limit must be positive", ErrInvalidInput)
	}
	now := s.now()
	refs, err := s.store.ExpiredItems(ctx, now.Add(-period), period, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired items: %w", err)
	}
	counts := map[uuid.UUID]int{}
	var first error
	failed := 0
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return counts, fmt.Errorf("forget expired items: %w", err)
		}
		forgot, err := s.store.ForgetItem(ctx, ref, ForgetSteps{
			Proceed: func(n ItemNewest) bool { return itemExpired(n, now, period) },
			Within:  s.closeAlerts(ctx, ref, now),
		})
		if err != nil {
			failed++
			if first == nil {
				first = fmt.Errorf("forget %s/%q on host %s: %w", ref.CheckKey, ref.Item, ref.HostID, err)
			}
			continue
		}
		if forgot {
			counts[ref.OrgID]++
		}
	}
	if first != nil {
		return counts, fmt.Errorf("forget expired items: %d of %d failed, first: %w", failed, len(refs), first)
	}
	return counts, nil
}

// closeAlerts is the Within step both entry points hand the store.
func (s *Service) closeAlerts(ctx context.Context, ref ItemRef, now time.Time) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		if _, err := s.alerts.CloseForgotten(ctx, tx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, now); err != nil {
			return fmt.Errorf("close alerts: %w", err)
		}
		return nil
	}
}
