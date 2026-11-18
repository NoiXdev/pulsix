package checks

// Forgetting one metric of an item (backlog 3.88, spec
// docs/superpowers/specs/2026-11-18-vanished-metrics-and-assertion-actions-design.md
// item 5). A check that keeps reporting on an item but no longer reports
// one of its metrics — a timing metric of a probe whose redirect chain
// changed, a metric a removed parameter switched off — leaves that
// metric's last value in check_current, and the item expiry never removes
// it because the item's other metrics keep the item fresh. The rules stop
// judging it once it has vanished (rules.Vanished); after the item expiry
// period behind its own item the hourly expiry forgets it, and the host
// page can forget it at once. Forgetting a metric removes its current
// value, its rule state and state-change history, and closes its open
// alert as forgotten. The item's other metrics, its discovery row
// (check_items), samples and item decisions are untouched.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrLastMetric refuses a manual metric forget that would remove its
// item's last current row (backlog 3.88, final review): the item's
// discovery row (check_items) would stay behind with nothing that could
// ever remove it — the item expiry and the item forget both start from
// check_current. Forgetting the whole item is the way. The metric expiry
// never meets it: an item's newest error-free reading is never due, and
// an item with none has nothing due at all.
var ErrLastMetric = errors.New("this is the item's last value; forget the item instead")

// MetricRef names one metric of one item of one host.
type MetricRef struct {
	OrgID, HostID          uuid.UUID
	CheckKey, Item, Metric string
}

// MetricNewest is what the store reads about a metric inside its forget
// transaction, after the host's evaluation lock: whether the metric still
// has a current row, when that reading was taken, the newest error-free
// reading of its item over all of the item's metrics, and how many other
// current rows (Siblings) the item has, error rows included. Metric is
// meaningless when Found is false; Item is the Unix epoch when the item
// has no error-free reading at all — only an error-free reading proves
// the item still reports (rules.Vanished's doc comment).
type MetricNewest struct {
	Found        bool
	Metric, Item time.Time
	Siblings     int
}

// MetricForgetSteps are ForgetSteps for one metric: Proceed (optional)
// runs after the host's evaluation lock is held and false abandons the
// forget with nothing changed; Within runs last on the same transaction —
// the alert close — and an error from it rolls the forget back.
type MetricForgetSteps struct {
	Proceed func(MetricNewest) bool
	Within  func(tx pgx.Tx) error
}

// metricExpired is the metric expiry's rule (ListExpiredMetrics is the
// same rule in SQL, for the selection): the metric's reading is older than
// its item's newest error-free reading by strictly more than period. The
// item's newer reading is the proof that the check still reports the
// item; a whole item that went quiet together has none, and is the item
// expiry's to judge, and an item with nothing but errors has none either.
func metricExpired(n MetricNewest, period time.Duration) bool {
	return n.Found && n.Metric.Before(n.Item.Add(-period))
}

// ForgetMetric forgets one metric of one item of hostID at once (the host
// page's Forget on a vanished row): its current value, rule state and
// state-change history, and its open alert, closed as forgotten — in one
// transaction. The item's other metrics and its discovery row stay.
// ErrNotFound means the metric has no current value to forget (never
// reported, already forgotten, or its row being written by an ingest at
// this very moment, which then lands after the forget anyway).
// ErrLastMetric means it is its item's last current row, checked under
// the host's evaluation lock: forget the item instead. item may be "", the
// whole-host item; checkKey and metric are required.
func (s *Service) ForgetMetric(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item, metric string) error {
	if s.alerts == nil {
		return errNoAlertCloser
	}
	if checkKey == "" || metric == "" {
		return fmt.Errorf("%w: check_key and metric are required", ErrInvalidInput)
	}
	ref := MetricRef{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item, Metric: metric}
	last := false
	forgot, err := s.store.ForgetMetric(ctx, ref, MetricForgetSteps{
		Proceed: func(n MetricNewest) bool {
			last = n.Found && n.Siblings == 0
			return !last
		},
		Within: s.closeMetricAlerts(ctx, ref, s.now()),
	})
	if err != nil {
		return fmt.Errorf("forget metric: %w", err)
	}
	if last {
		return ErrLastMetric
	}
	if !forgot {
		return ErrNotFound
	}
	return nil
}

// ForgetExpiredMetrics forgets up to limit metrics, across every
// organization, whose reading is older than their own item's newest by
// more than period (metricExpired). Each metric is forgotten in a
// transaction of its own, and the rule is checked again inside it, after
// the host's evaluation lock: a metric that reported again between the
// selection and its forget is left alone. Like ForgetExpired, a failing
// metric does not stop the others; the error names how many failed and the
// first failure, next to the per-organization counts of what succeeded.
func (s *Service) ForgetExpiredMetrics(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error) {
	if s.alerts == nil {
		return nil, errNoAlertCloser
	}
	if period <= 0 || limit <= 0 {
		return nil, fmt.Errorf("%w: period and limit must be positive", ErrInvalidInput)
	}
	now := s.now()
	refs, err := s.store.ExpiredMetrics(ctx, period, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired metrics: %w", err)
	}
	counts := map[uuid.UUID]int{}
	var first error
	failed := 0
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return counts, fmt.Errorf("forget expired metrics: %w", err)
		}
		forgot, err := s.store.ForgetMetric(ctx, ref, MetricForgetSteps{
			Proceed: func(n MetricNewest) bool { return metricExpired(n, period) },
			Within:  s.closeMetricAlerts(ctx, ref, now),
		})
		if err != nil {
			failed++
			if first == nil {
				first = fmt.Errorf("forget %s/%q/%s on host %s: %w", ref.CheckKey, ref.Item, ref.Metric, ref.HostID, err)
			}
			continue
		}
		if forgot {
			counts[ref.OrgID]++
		}
	}
	if first != nil {
		return counts, fmt.Errorf("forget expired metrics: %d of %d failed, first: %w", failed, len(refs), first)
	}
	return counts, nil
}

// closeMetricAlerts is the Within step both metric entry points hand the
// store.
func (s *Service) closeMetricAlerts(ctx context.Context, ref MetricRef, now time.Time) func(pgx.Tx) error {
	return func(tx pgx.Tx) error {
		if _, err := s.alerts.CloseForgottenMetric(ctx, tx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item, ref.Metric, now); err != nil {
			return fmt.Errorf("close alerts: %w", err)
		}
		return nil
	}
}
