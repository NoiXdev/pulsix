package checks

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Observation is one metric or discrete reading an agent reports for a
// host, tagged with the check that produced it and stamped with the
// agent's own clock — never arrival time, since replay after a
// reconnect needs a timestamp comparable across the gap ("Sample
// delivery").
//
// Observation is keyed by CheckKey (text), never by a check_definitions
// row id: a definition can be edited or deleted without orphaning or
// rewriting history, and the key is what an operator recognises on a
// graph. RecordObservations therefore never looks check_key up against
// check_definitions at all — an Observation for a key with no matching
// definition (deleted, or never created) is stored exactly like any
// other. Item is "" for a whole-host metric (cpu, memory); a per-item
// metric (disk, service) names the mount or unit.
//
// Exactly one of Value, Label or Err is meaningful for a given
// Observation: Err set means collection failed on the agent and no
// check_samples row is written at all — only check_current's error
// column changes, so the host page can show "collection failed" instead
// of a stale number that looks fresh. Otherwise exactly one of Value
// (numeric) or Label (discrete) is set, never both, never neither.
// RecordObservations rejects (counts, never errors the batch for) an
// Observation whose shape matches neither case, whose At falls outside
// the plausibility window, or whose HostID does not belong to the
// organization.
type Observation struct {
	HostID                 uuid.UUID
	CheckKey, Item, Metric string
	At                     time.Time
	Value                  *float64
	Label                  *string
	Err                    string
}

// Current is the latest known value of one (check_key, item, metric)
// tuple on a host — what the host page's monitoring section renders
// without scanning the hypertable.
//
// A Current row is never deleted when the thing it measures disappears —
// a disk unmounted, a service uninstalled: nothing observes a
// "disappearance", the agent simply stops sending that tuple. This
// mirrors how a fact tag survives its fact (internal/hosts/tags.go): the
// row is left exactly as it was last reported, and its own At is what
// makes it recognisable as stale. A later task renders any Current older
// than three times the check's own interval as stale rather than
// current; this package makes no attempt to guess an interval or delete
// anything on its own. Do not "fix" RecordObservations to delete a
// Current row for a key/item/metric a batch no longer mentions — that is
// the one behaviour this type exists to prevent.
type Current struct {
	CheckKey, Item, Metric string
	At                     time.Time
	Value                  *float64
	Label                  *string
	Err                    string
}

// Point is one bucketed sample of a Series.
type Point struct {
	At    time.Time
	Value float64
}

// Series is one metric's values over a time range, already bucketed
// server-side.
type Series struct {
	Metric string
	Points []Point
}

// Plausibility window ("Sample delivery"): a sample timestamped further in
// the future than maxFuture, or older than maxPast, says more about the
// agent's clock than about reality and is rejected rather than trusted.
// maxPast is generous — the agent's own bounded replay ring holds 15
// minutes, maxPast adds slack for a slow reconnect — so this window is a
// sanity check on the agent's clock, not a tight replay-window mirror.
const (
	maxFuture = 2 * time.Minute
	maxPast   = 1 * time.Hour
)

// plausible reports whether at, compared against now, falls inside the
// plausibility window. Both boundaries are inclusive: exactly maxFuture
// ahead or exactly maxPast behind is accepted, one second further either
// way is not (pinned by TestRecordObservationsPlausibilityWindowBoundaries).
func plausible(at, now time.Time) bool {
	return !at.After(now.Add(maxFuture)) && !at.Before(now.Add(-maxPast))
}

// The check-results contract's bounds — the ingest path's mirror of the
// facts contract's own MaxFactsKeys/MaxFactsValueLen
// (internal/agents/facts.go), and enforced the same way: a batch over any
// of them is REFUSED with ErrInvalidInput, never clipped and never
// partially stored. The distinction from the plausibility window and the
// shape check (which COUNT a bad observation and keep the rest of the
// batch) is deliberate and is about who is at fault. A skewed clock or a
// host deleted mid-flight is something a correct agent can produce; a
// check_key longer than any key the manager will ever push, or fifty
// thousand observations in one message, is not — every field here
// originates either in the manager's own CheckConfig (check_key) or in a
// compile-time constant in the agent's plugins (metric), so a violation
// means the agent is broken or lying, and lying input gets a refusal.
//
// The numbers, and where each comes from:
//
//   - MaxCheckKeyLen: a check_key is the key the manager itself pushed in
//     CheckConfig, and normalizeDefinition's keyPattern caps that at 63
//     bytes. 64 is that bound with a byte of slack.
//
//   - MaxItemLen: an item is a disk mount point or a systemd unit name.
//     Unit names are capped at 256 bytes by systemd and a path component
//     at 255 by the kernel; 512 covers the deep bind-mount paths a
//     container host really does carry, and is the same figure
//     MaxFactsValueLen already uses for the same kind of "verbatim, but
//     bounded" string.
//
//   - MaxMetricLen: metric names are literals in the agent's plugins —
//     the longest today is "disk.inodes_used_pct" at 20 bytes. 64 leaves
//     room for whatever a later plugin names its metrics.
//
// Together those three cap one row's contribution to
// check_samples_lookup_idx — a btree over (org_id, host_id, check_key,
// item, metric, at) whose index rows may not exceed 8191 bytes — at
// 16+16+64+512+64+8 = 680 bytes plus per-row overhead, an order of
// magnitude clear of the limit. Before these bounds existed, an
// agent-chosen key of a few kilobytes produced "index row requires N
// bytes, maximum size is 8191" from inside RecordObservations'
// transaction — 9064 bytes for 3x3000 incompressible ones, measured
// against the container by TestLookupIndexRejectsWhatTheContractBoundsForbid.
// That is a store error, not a count: the whole batch rolled back and the
// stream died with Internal. Measure it with incompressible strings or
// not at all — PostgreSQL compresses a btree index entry's varlena
// values, so 3x3000 REPEATED bytes insert quite happily.
//
//   - MaxObservationsPerBatch: the cost side. RecordObservations issues
//     two statements per observation, serially, inside one transaction on
//     one pool connection; 10 000 observations measured 11.98 s of that,
//     which is a denial of service against every other tenant sharing the
//     pool. The bound has to clear the largest batch a legitimate host can
//     produce, and one message carries exactly one Collect's output
//     ("Sample delivery": one message per batch): the seed set's cpu (4
//     observations), memory (2) and service (one per unit) are small, and
//     only disk fans out — three metrics per mount, so 1000 still covers a
//     host with 333 filesystems. The 15-minute replay ring cannot exceed
//     it either: the agent chunks both replayed and live results at
//     maxResultsPerMessage (200, internal/agent/session.go), five times
//     under this bound, so a well-behaved agent never approaches it no
//     matter how long it was disconnected — a relation the agent package's
//     own TestMaxResultsPerMessageStaysUnderTheIngestBound holds against
//     this constant directly, rather than leaving both comments to drift.
const (
	MaxObservationsPerBatch = 1000
	MaxCheckKeyLen          = 64
	MaxItemLen              = 512
	MaxMetricLen            = 64
)

// withinContract reports whether o's identifying strings are inside the
// check-results contract's bounds. An empty item is legal — that is what
// a whole-host metric reports — but an empty check_key or metric names
// nothing that could ever be read back while still occupying a permanent
// check_current row, so both are refused, exactly as RecordFacts refuses
// an empty fact key. All three must be valid UTF-8 (backlog 7.52): they are
// proto3 strings on the agent's wire and text in the store, and an agent's
// own gRPC stack cannot even encode one that is not.
func withinContract(o Observation) error {
	switch {
	case o.CheckKey == "":
		return fmt.Errorf("%w: empty check_key", ErrInvalidInput)
	case o.Metric == "":
		return fmt.Errorf("%w: empty metric for check_key %q", ErrInvalidInput, o.CheckKey)
	case len(o.CheckKey) > MaxCheckKeyLen:
		return fmt.Errorf("%w: check_key exceeds %d bytes", ErrInvalidInput, MaxCheckKeyLen)
	case len(o.Item) > MaxItemLen:
		return fmt.Errorf("%w: item for check_key %q exceeds %d bytes", ErrInvalidInput, o.CheckKey, MaxItemLen)
	case len(o.Metric) > MaxMetricLen:
		return fmt.Errorf("%w: metric for check_key %q exceeds %d bytes", ErrInvalidInput, o.CheckKey, MaxMetricLen)
	case !utf8.ValidString(o.CheckKey) || !utf8.ValidString(o.Item) || !utf8.ValidString(o.Metric):
		return fmt.Errorf("%w: check_key, item and metric must be valid UTF-8", ErrInvalidInput)
	case breaksLine(o.CheckKey) || breaksLine(o.Item) || breaksLine(o.Metric):
		return fmt.Errorf("%w: check_key, item and metric must not contain control characters or line breaks", ErrInvalidInput)
	}
	return nil
}

// breaksLine reports whether s holds a control character or a Unicode line
// or paragraph separator. The agent chooses check_key, item and metric, and
// alert notifications print them one alert per line, so such a character
// would let a host forge alert rows or chat markup in mails and webhook
// bodies (backlog 7.13). The sibling name validators refuse the same.
func breaksLine(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) >= 0
}

// wellShaped reports whether o carries exactly one of the shapes
// RecordObservations ever stores: a collection error with no value or
// label, or a value/label sample with no error and exactly one of the
// two set. Any other combination (both value and label, neither, or an
// error alongside a value/label) cannot be represented by either table's
// own CHECK constraint and is rejected rather than attempted.
func wellShaped(o Observation) bool {
	if o.Err != "" {
		return o.Value == nil && o.Label == nil
	}
	return (o.Value != nil) != (o.Label != nil)
}

// Ingest is what one RecordObservations batch did, with every rejection
// attributed to the rule that rejected it rather than summed into one
// number. The three causes are three different things to do about them,
// and an operator reading a log line is the only consumer this milestone
// has (there is no metrics endpoint in Pulsix yet — backlog 3.27):
//
//   - Implausible: the observation's own timestamp falls outside the
//     plausibility window. Nothing is wrong with the agent's checks; its
//     CLOCK disagrees with the manager's. The remedy is NTP on that host.
//   - Malformed: the observation is not a shape either table can store
//     (a value and a label together, an error beside either, or neither).
//     No correct agent build produces one, so this means a broken or
//     lying agent.
//   - UnknownHost: the store recognised neither the host nor its
//     organization. Ordinary right after a host is deleted while its
//     agent is still connected; persistent means a stale enrolment.
//
// Accepted + Rejected() == len(obs) always.
type Ingest struct {
	Accepted    int
	Implausible int
	Malformed   int
	UnknownHost int
}

// Rejected is the total across all three causes — what a caller reports
// when it only has room for one number.
func (i Ingest) Rejected() int { return i.Implausible + i.Malformed + i.UnknownHost }

// RecordObservations validates and stores one ingest batch for orgID.
// Every Observation is checked against its own shape and the plausibility
// window; what survives is handed to the store, which applies the one
// rejection rule that needs a database lookup — an unknown HostID. Every
// kind of rejection is counted, never turned into an error for the
// batch: one skewed clock or one host that was deleted mid-flight must
// never lose every other Observation in the same batch. err is non-nil
// only for a genuine store failure (the batch's fate is then unknown, not
// "all rejected").
//
// One statement in that store transaction belongs to somebody else: every
// host a batch actually stores anything for is also marked dirty in
// check_eval_cursor (MarkHostDirty), which is how the rule engine's
// ticker — internal/rules, never called from here — learns that the host
// has samples nothing has judged yet. It lives inside the ingest
// transaction rather than after it because a mark written afterwards could
// be lost, leaving a batch stored and never evaluated; it is one upsert
// per host per batch, so the ~7 000 samples/s hot path pays a single
// primary-key probe for it.
//
// The contract's own bounds are the one exception to "counted, never an
// error", and they are checked BEFORE the store is reached at all — see
// MaxObservationsPerBatch's comment for why a violation of them is the
// agent's fault rather than the world's, and why a batch over them must
// not be allowed to cost a transaction on a shared pool connection in the
// first place. ErrInvalidInput from here is what agentapi maps to
// InvalidArgument.
func (s *Service) RecordObservations(ctx context.Context, orgID uuid.UUID, obs []Observation) (Ingest, error) {
	var res Ingest
	if len(obs) == 0 {
		return res, nil
	}
	if len(obs) > MaxObservationsPerBatch {
		return res, fmt.Errorf("%w: at most %d observations per batch, got %d",
			ErrInvalidInput, MaxObservationsPerBatch, len(obs))
	}
	for _, o := range obs {
		if err := withinContract(o); err != nil {
			return Ingest{}, err
		}
	}
	now := s.now()
	valid := make([]Observation, 0, len(obs))
	for _, o := range obs {
		// Shape first: an observation that carries both a value and a
		// label says nothing worth checking a timestamp against, and
		// counting it as a clock problem would send an operator to NTP
		// for an agent bug.
		switch {
		case !wellShaped(o):
			res.Malformed++
		case !plausible(o.At, now):
			res.Implausible++
		default:
			valid = append(valid, o)
		}
	}
	if len(valid) == 0 {
		return res, nil
	}
	// What the batch says about the agent's clock, measured before the
	// store is reached: this is the only moment the manager ever holds an
	// agent's own timestamps next to its own clock, and nothing persists
	// the comparison (skew.go explains what is kept and why here).
	s.offsets.note(valid, now)
	accepted, unknownHost, err := s.store.RecordObservations(ctx, orgID, valid)
	if err != nil {
		return Ingest{}, fmt.Errorf("record observations: %w", err)
	}
	res.Accepted, res.UnknownHost = accepted, unknownHost
	return res, nil
}

// CurrentForHost returns hostID's latest known value per (check_key,
// item, metric), an entry per tuple ever reported — including one whose
// item has since disappeared (Current's own doc comment).
func (s *Service) CurrentForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Current, error) {
	return s.store.CurrentForHost(ctx, orgID, hostID)
}

// SeriesForHost returns metric's samples for hostID's (checkKey, item) in
// [from, to), downsampled server-side into at most buckets points.
func (s *Service) SeriesForHost(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item, metric string, from, to time.Time, buckets int) (Series, error) {
	if buckets < 1 {
		return Series{}, fmt.Errorf("%w: buckets must be at least 1", ErrInvalidInput)
	}
	if !to.After(from) {
		return Series{}, fmt.Errorf("%w: to must be after from", ErrInvalidInput)
	}
	return s.store.SeriesForHost(ctx, orgID, hostID, checkKey, item, metric, from, to, buckets)
}
