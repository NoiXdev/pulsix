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

// DetailSource says why tuple alerts fired. Apply asks it for a
// DetailLookup once per evaluation and then asks that about each subject;
// every subject of one Apply belongs to one host, so a lookup may read
// what it needs about the host once and reuse it. Declared here, next to
// Service, so this package never imports what implements it.
type DetailSource interface {
	Lookup() DetailLookup
}

// DetailLookup is one Apply's lookups. Detail answers for one subject: ""
// with a nil error when there is nothing to say, an error when the lookup
// failed — which Apply tells apart (captureDetails). tx is the evaluator's
// own transaction, inside a savepoint Apply has set: every read sees the
// tick's own writes, and whatever fails there is rolled back with the
// savepoint alone. The lookup must not commit or roll back tx itself.
type DetailLookup interface {
	Detail(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, sub Subject) (string, error)
}

// SetDetailSource wires the port Apply captures details through, and the
// logger a failed capture is reported on. A Service without one (every
// Service a test builds with NewService alone) captures nothing and sets
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
//
// A lookup that failed is not "nothing to say": on a severity change it
// sets KeepDetail, so the alert keeps the reason it had rather than losing
// it to a transient fault, while the change's events carry none.
func (s *Service) captureDetails(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, open []Alert, actions []Action) {
	if s.details == nil {
		return
	}
	var lookup DetailLookup
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
		if lookup == nil {
			lookup = s.details.Lookup()
		}
		d, ok := s.detail(ctx, tx, lookup, orgID, sub)
		actions[i].Detail = d
		actions[i].KeepDetail = !ok && a.Kind == ActionSeverity
	}
}

// detail asks lookup about one subject inside a savepoint of tx, and
// reports whether it succeeded. A failure — setting the savepoint, the
// lookup, or releasing it — is logged and yields no detail rather than
// failing the host's evaluation.
func (s *Service) detail(ctx context.Context, tx pgx.Tx, lookup DetailLookup, orgID uuid.UUID, sub Subject) (string, bool) {
	d, err := s.lookup(ctx, tx, lookup, orgID, sub)
	if err != nil {
		s.log.WarnContext(ctx, "alert detail lookup failed; the alert carries no new detail",
			"org_id", orgID, "host_id", sub.HostID, "check_key", sub.CheckKey,
			"item", sub.Item, "metric", sub.Metric, "error", err)
		return "", false
	}
	return cutDetail(d), true
}

// detailSavepoint names the savepoint lookup sets. It is named and driven
// by hand rather than through pgx's nested transaction, whose Commit marks
// it closed even when the RELEASE fails — after which nothing could roll
// back to it. A RELEASE fails exactly when the lookup left the transaction
// aborted (a source that swallowed an SQL error), and that is the case the
// savepoint exists for.
const detailSavepoint = "alert_detail"

// lookup runs one lookup between SAVEPOINT and RELEASE. When the lookup
// fails, or the RELEASE does, it rolls back to the savepoint — undoing
// whatever the lookup did and clearing an aborted state — and releases it,
// so the evaluator's transaction is left exactly as usable as it was. The
// rollback runs on a context that ignores cancellation, like every other
// rollback in this package.
func (s *Service) lookup(ctx context.Context, tx pgx.Tx, lookup DetailLookup, orgID uuid.UUID, sub Subject) (string, error) {
	if _, err := tx.Exec(ctx, "SAVEPOINT "+detailSavepoint); err != nil {
		return "", fmt.Errorf("set savepoint: %w", err)
	}
	d, err := lookup.Detail(ctx, tx, orgID, sub)
	if err == nil {
		if _, err = tx.Exec(ctx, "RELEASE SAVEPOINT "+detailSavepoint); err == nil {
			return d, nil
		}
		err = fmt.Errorf("release savepoint: %w", err)
	}
	bg := context.WithoutCancel(ctx)
	if _, rerr := tx.Exec(bg, "ROLLBACK TO SAVEPOINT "+detailSavepoint); rerr != nil {
		return "", errors.Join(err, fmt.Errorf("roll back to the savepoint: %w", rerr))
	}
	if _, rerr := tx.Exec(bg, "RELEASE SAVEPOINT "+detailSavepoint); rerr != nil {
		return "", errors.Join(err, fmt.Errorf("release the rolled-back savepoint: %w", rerr))
	}
	return "", err
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
