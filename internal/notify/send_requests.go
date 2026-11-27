package notify

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// defaultSendBudget bounds the requests of one delivery: half the lease, so
// a slow service never outlasts the lease (another manager would claim the
// row and send it again) and never holds the dispatcher's batch, and every
// other channel claimed with it, for longer (backlog 4.8 review I2).
const defaultSendBudget = leaseFor / 2

// SenderOption configures a ChannelSender.
type SenderOption func(*ChannelSender)

// WithAlertStates lets an AlertStateAware preset read alerts' current
// state at send time.
func WithAlertStates(a AlertStates) SenderOption { return func(s *ChannelSender) { s.states = a } }

// WithSendBudget replaces the bound on one delivery's requests (tests).
func WithSendBudget(d time.Duration) SenderOption {
	return func(s *ChannelSender) {
		if d > 0 {
			s.budget = d
		}
	}
}

// PartialSendError is a delivery of several requests that left some owed
// (backlog 4.8): Pending names them by requestKey (preset and key), so the retry sends
// only those and never one that already succeeded — the request-level
// counterpart of mail.PartialError's recipients.
type PartialSendError struct {
	Pending []string
	Err     error
}

func (e *PartialSendError) Error() string { return e.Err.Error() }
func (e *PartialSendError) Unwrap() error { return e.Err }

// closedAlerts reads, for an AlertStateAware preset rendering an alert
// batch, which of its alerts are closed now.
func (s *ChannelSender) closedAlerts(ctx context.Context, w openedWebhook, orgID uuid.UUID, ev Event) (map[uuid.UUID]bool, error) {
	if s.states == nil || w.preset == "" || ev.Kind != EventAlertBatch || ev.Alerts == nil || len(ev.Alerts.Items) == 0 {
		return nil, nil
	}
	if _, ok := s.presets[w.preset].(AlertStateAware); !ok {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(ev.Alerts.Items))
	for _, it := range ev.Alerts.Items {
		ids = append(ids, it.AlertID)
	}
	return s.states.ClosedAlerts(ctx, orgID, ids)
}

// reserve is the time one request may take (the client's own timeout): a
// request is only started while that much of the budget is left, so the
// budget never cuts one off mid-flight.
func (s *ChannelSender) reserve() time.Duration {
	if s.http != nil && s.http.Timeout > 0 {
		return s.http.Timeout
	}
	return 10 * time.Second
}

// requestKey is how a delivery's pending list names a request: the
// preset's own key (its position when it has none), prefixed with the
// preset's name ("webhook" for a raw channel), so the keys one preset
// stored never match a request of another after the channel is switched
// (re-review N1).
func requestKey(preset string, r Request, i int) string {
	if preset == "" {
		preset = "webhook"
	}
	k := r.Key
	if k == "" {
		k = strconv.Itoa(i)
	}
	return preset + ":" + k
}

type requestFailure struct {
	n   int
	err error
}

// sendAll posts the requests in order. A lone request keeps the plain
// webhook semantics: its error is the delivery's. Several are each tried
// even when one fails (one refused event must not drop the rest), within
// the send budget, and only those a retry still owes are sent: the
// delivery's pending list (PendingRecipients, backlog 2.74) names them
// by key. What is left — failed or not reached in time — comes back as a
// PartialSendError naming it; permanent only when every request left
// failed permanently.
func (s *ChannelSender) sendAll(ctx context.Context, w openedWebhook, reqs []Request, c Claimed, ev Event) error {
	if len(reqs) == 0 {
		return nil // nothing to say for this event (PagerDuty: an unacknowledged alert)
	}
	owed := owedKeys(c.Delivery.PendingRecipients, w.preset, reqs)
	if len(reqs) == 1 && len(owed) == 0 {
		return s.sendRequest(ctx, w, reqs[0], c.Delivery.ID, ev)
	}
	ctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	var (
		failures  []requestFailure
		pending   []string
		unsent    int
		transient bool
	)
	for i, r := range reqs {
		key := requestKey(w.preset, r, i)
		if len(owed) > 0 && !owed[key] {
			continue
		}
		if dl, _ := ctx.Deadline(); time.Until(dl) < s.reserve() || ctx.Err() != nil {
			pending, unsent = append(pending, key), unsent+1
			continue
		}
		if err := s.sendRequest(ctx, w, r, c.Delivery.ID, ev); err != nil {
			failures, pending = append(failures, requestFailure{n: i + 1, err: err}), append(pending, key)
			transient = transient || !IsPermanent(err)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	perr := &PartialSendError{Pending: pending, Err: sendSummary(failures, unsent, len(reqs))}
	if !transient && unsent == 0 {
		return Permanent(perr)
	}
	return perr
}

// owedKeys is the delivery's pending list as far as it names requests
// rendered now. When it names none of them — the channel switched preset
// since, or the list predates the keys — nothing this render produces
// was ever sent, so every request is owed (nil): the alternative is a
// delivery completed with nothing sent (re-review N1).
func owedKeys(pending []string, preset string, reqs []Request) map[string]bool {
	if len(pending) == 0 {
		return nil
	}
	rendered := make(map[string]bool, len(reqs))
	for i, r := range reqs {
		rendered[requestKey(preset, r, i)] = true
	}
	owed := map[string]bool{}
	for _, k := range pending {
		if rendered[k] {
			owed[k] = true
		}
	}
	if len(owed) == 0 {
		return nil
	}
	return owed
}

// maxNamedFailures bounds how many failed requests a delivery's error
// names one by one.
const maxNamedFailures = 5

// sendSummary is a partial send's error text: which requests failed and
// why (status codes and transport causes only, never a URL or a secret),
// and how many the attempt's time did not reach.
func sendSummary(failures []requestFailure, unsent, total int) error {
	parts := make([]string, 0, maxNamedFailures+2)
	for i, f := range failures {
		if i == maxNamedFailures {
			parts = append(parts, fmt.Sprintf("%d more failed", len(failures)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("request %d: %v", f.n, f.err))
	}
	if unsent > 0 {
		parts = append(parts, fmt.Sprintf("%d not sent before this attempt's time ran out", unsent))
	}
	return fmt.Errorf("%d of %d requests still owed: %s", len(failures)+unsent, total, strings.Join(parts, "; "))
}
