// The alert detail (backlog 3.13b, spec docs/superpowers/specs/2026-11-16-
// web-assertions-design.md, "4. Alert detail"): why a tuple alert fired —
// a synthetic check's failure reason, or a failed web assertion with the
// value it saw — captured when Apply opens the alert or changes its
// severity, and stored with the alert and with the events of that action.
// What the detail says is the DetailSource's business (internal/app
// implements it over the checks store and service); this file decides
// when to ask, and makes sure that asking can never break the evaluation.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxDetailBytes bounds one detail. Longer ones are cut on a rune
// boundary.
const MaxDetailBytes = 512

// DetailSource says why a tuple alert on sub fired, "" when there is
// nothing to say. tx is a savepoint of the evaluator's own transaction:
// every database read the source makes on it sees the tick's own writes,
// and a statement that fails there is rolled back with the savepoint
// alone. Declared here, next to Service, so this package never imports
// what implements it.
type DetailSource interface {
	Detail(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub Subject) (string, error)
}

// SetDetailSource wires the port Apply captures details through, and the
// logger a failed capture is reported on. A Service without one (every
// Service a test builds with NewService alone) captures nothing and takes
// no savepoint. Post-construction for the same reason as SetNotifyLedger:
// the source is built from the checks service, which itself is built
// after this one (internal/app.Build).
func (s *Service) SetDetailSource(src DetailSource, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.details, s.log = src, log
}

// captureDetails fills Detail on every action that opens a tuple alert or
// changes its severity. A severity action carries only the alert's id
// (Reconcile's contract), so its subject comes from open — the alerts
// Reconcile was given; only a tuple alert ever changes severity, but the
// scope is checked anyway. Closes and flaps carry no detail, and neither
// does the host-scope alert, which has no tuple to explain.
func (s *Service) captureDetails(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, open []Alert, actions []Action) {
	if s.details == nil {
		return
	}
	var byID map[uuid.UUID]Subject
	for i, a := range actions {
		var sub Subject
		switch a.Kind {
		case ActionOpen:
			sub = a.Subject
		case ActionSeverity:
			if byID == nil {
				byID = make(map[uuid.UUID]Subject, len(open))
				for _, o := range open {
					byID[o.ID] = o.Subject
				}
			}
			found := false
			if sub, found = byID[a.AlertID]; !found {
				continue
			}
		default:
			continue
		}
		if sub.Scope != ScopeTuple {
			continue
		}
		actions[i].Detail = s.detail(ctx, tx, orgID, sub)
	}
}

// detail asks the source about one subject inside a savepoint of tx. A
// failure — the savepoint, the lookup or the release — is logged and
// yields no detail: the alert opens or moves without one rather than
// failing the host's evaluation, and the rollback to the savepoint undoes
// whatever the lookup did, so a failed statement does not leave the
// evaluator's transaction aborted.
func (s *Service) detail(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub Subject) string {
	d, err := s.lookup(ctx, tx, orgID, sub)
	if err != nil {
		s.log.WarnContext(ctx, "alert detail lookup failed; the alert carries no detail",
			"org_id", orgID, "host_id", sub.HostID, "check_key", sub.CheckKey,
			"item", sub.Item, "metric", sub.Metric, "error", err)
		return ""
	}
	return cutDetail(d)
}

func (s *Service) lookup(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub Subject) (string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("savepoint: %w", err)
	}
	d, err := s.details.Detail(ctx, sp, orgID, sub)
	if err != nil {
		if rerr := sp.Rollback(context.WithoutCancel(ctx)); rerr != nil {
			return "", errors.Join(err, fmt.Errorf("roll back the savepoint: %w", rerr))
		}
		return "", err
	}
	if err := sp.Commit(ctx); err != nil {
		return "", fmt.Errorf("release savepoint: %w", err)
	}
	return d, nil
}

// cutDetail cuts d to MaxDetailBytes, never inside a rune.
func cutDetail(d string) string {
	if len(d) <= MaxDetailBytes {
		return d
	}
	i := MaxDetailBytes
	for i > 0 && !utf8.RuneStart(d[i]) {
		i--
	}
	return d[:i]
}
