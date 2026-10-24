package notify

import (
	"time"

	"github.com/google/uuid"
)

// Category is what a schedule rule listens for (spec §7): failure,
// change (at least one host reported changed), success, skipped
// (skipped_* and missed decisions).
type Category string

const (
	CategoryFailure Category = "failure"
	CategoryChange  Category = "change"
	CategorySuccess Category = "success"
	CategorySkipped Category = "skipped"
)

// Categories lists every category in its canonical order.
func Categories() []Category {
	return []Category{CategoryFailure, CategoryChange, CategorySuccess, CategorySkipped}
}

// ParseCategory accepts exactly the four category names.
func ParseCategory(s string) (Category, bool) {
	for _, c := range Categories() {
		if string(c) == s {
			return c, true
		}
	}
	return "", false
}

// EventKind names what a message is about.
type EventKind string

const (
	EventRunFinished     EventKind = "run.finished"
	EventScheduleSkipped EventKind = "schedule.skipped"
	EventScheduleFailed  EventKind = "schedule.failed"
	EventDigest          EventKind = "digest.weekly"
	EventTest            EventKind = "test"
	EventAlertBatch      EventKind = "alert.batch"
)

// Event is the fixed structure every message is rendered from — the
// default webhook body is its JSON form, a body template reads its
// fields. It deliberately has no field for log output, variables or
// secrets. Nil sections are absent for the kind.
type Event struct {
	Kind       EventKind        `json:"kind"`
	At         time.Time        `json:"at"`
	Org        OrgRef           `json:"org"`
	Categories []Category       `json:"categories,omitempty"`
	Schedule   *NamedRef        `json:"schedule,omitempty"`
	Template   *NamedRef        `json:"template,omitempty"`
	Run        *RunSummary      `json:"run,omitempty"`
	Decision   *DecisionSummary `json:"decision,omitempty"`
	Digest     *DigestSummary   `json:"digest,omitempty"`
	Alerts     *AlertBatch      `json:"alerts,omitempty"`
	URL        string           `json:"url,omitempty"`
}

// OrgRef names the organization a message is about.
type OrgRef struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// NamedRef is a schedule or template by id and name.
type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RunSummary is a finished run group: its result (succeeded, failed,
// cancelled), host counts from the stored per-host results, and timing.
type RunSummary struct {
	ID           uuid.UUID `json:"id"`
	Result       string    `json:"result"`
	Hosts        int       `json:"hosts"`
	HostsOK      int       `json:"hosts_ok"`
	HostsChanged int       `json:"hosts_changed"`
	HostsFailed  int       `json:"hosts_failed"`
	// HostsNoResult counts targeted hosts the run reached no per-host
	// verdict on, because the job covering them ended without a recap
	// (backlog 2.81). They are excluded from HostsOK rather than counted
	// as fine, so hosts_ok + hosts_failed + hosts_no_result == hosts.
	HostsNoResult int `json:"hosts_no_result"`
	// HostsExcluded counts hosts the run never targeted because a
	// maintenance window excluded them. They are NOT part of Hosts, so
	// without this the message described only what remained — "9 hosts
	// (ok 9, changed 0, failed 0)" with no sign that three more were
	// left out (backlog 2.81).
	HostsExcluded   int        `json:"hosts_excluded"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	DurationSeconds int64      `json:"duration_seconds"`
}

// DecisionSummary is a schedule occurrence that did not start a run.
type DecisionSummary struct {
	Outcome string    `json:"outcome"`
	Reason  string    `json:"reason,omitempty"`
	DueAt   time.Time `json:"due_at"`
}

// DigestSummary is the weekly digest's content (spec §7). Schedules
// holds at most MaxDigestSchedules entries (schedules.Service builds
// it): when the organization has more, Truncated is true and
// ScheduleCountTotal the true count — the renderers say "+N schedules
// without activity" for the difference, so a very large organization's
// digest still renders under maxBodyBytes instead of failing for ever.
type DigestSummary struct {
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	Timezone  string           `json:"timezone"`
	Schedules []DigestSchedule `json:"schedules"`
	Slowest   []DigestRun      `json:"slowest"`

	Truncated          bool `json:"truncated,omitempty"`
	ScheduleCountTotal int  `json:"schedule_count_total,omitempty"`
}

// DigestSchedule is one schedule's week.
type DigestSchedule struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Runs     int       `json:"runs"`
	Failures int       `json:"failures"`
	Skipped  int       `json:"skipped"`
}

// DigestRun is one of the week's slowest runs. RunID is the run's first
// job (the run view); the outbox turns it into URL.
type DigestRun struct {
	Schedule        string    `json:"schedule"`
	RunID           uuid.UUID `json:"run_id"`
	StartedAt       time.Time `json:"started_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	URL             string    `json:"url"`
}

// AlertBatch is one tick's alerts for one channel (spec v0.4b, decision 5:
// events are coalesced per channel, so one message lists what happened
// rather than one message per event). Count is the number of items before
// any truncation, so a reader can tell a complete list from a capped one.
type AlertBatch struct {
	Count int         `json:"count"`
	Items []AlertItem `json:"items"`
}

// AlertItem is one alert event in a batch. It names what happened and
// where, and nothing else: no log output, no variables, no secrets, the
// same promise the rest of this package makes.
type AlertItem struct {
	AlertID  uuid.UUID `json:"alert_id"`
	HostID   uuid.UUID `json:"host_id"`
	HostName string    `json:"host_name"`
	CheckKey string    `json:"check_key"`
	Item     string    `json:"item,omitempty"`
	Metric   string    `json:"metric,omitempty"`
	Severity string    `json:"severity"`
	Event    string    `json:"event"`
	At       time.Time `json:"at"`
	URL      string    `json:"url,omitempty"`
}

// runCategories maps a finished run group to the categories it belongs
// to: its result (a cancelled run counts as a failure — it did not do its
// job) and change when any host reported changed.
func runCategories(result string, hostsChanged int) []Category {
	out := []Category{CategorySuccess}
	if result != "succeeded" {
		out = []Category{CategoryFailure}
	}
	if hostsChanged > 0 {
		out = append(out, CategoryChange)
	}
	return out
}

// decisionCategory maps a schedule decision that started no run to its
// category and event kind; ok is false for triggered (the run itself is
// reported when its group finishes).
func decisionCategory(outcome string) (Category, EventKind, bool) {
	switch outcome {
	case "skipped_overlap", "skipped_maintenance", "missed":
		return CategorySkipped, EventScheduleSkipped, true
	case "failed":
		return CategoryFailure, EventScheduleFailed, true
	}
	return "", "", false
}
