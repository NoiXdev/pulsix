package agents

import (
	"context"
	"time"
)

// HeartbeatInterval is the cadence the agent reports at by default
// (PULSIX_HEARTBEAT_INTERVAL, spec §8). An agent's online window is
// HeartbeatsPerOnlineWindow of its own interval: one missed heartbeat is
// a hiccup — a reconnect, a slow network — three is an agent that stopped
// reporting. OnlineWindowFloor, three default intervals, is the shortest
// window and the whole window of an agent that never reported its
// interval (older than the report, backlog 3.58), so such an agent is
// judged exactly as every agent was before. The verdict is computed at
// read time from last_seen, so nothing has to run to keep it current.
const (
	HeartbeatInterval         = 30 * time.Second
	HeartbeatsPerOnlineWindow = 3
	OnlineWindowFloor         = HeartbeatsPerOnlineWindow * HeartbeatInterval
)

// The agent's own bounds on PULSIX_HEARTBEAT_INTERVAL (internal/agent's
// config): a report outside them is not something a real agent sends.
const (
	minReportedHeartbeatInterval = 5 * time.Second
	maxReportedHeartbeatInterval = 10 * time.Minute
)

// OnlineWindow is how long after its last contact an agent still counts
// as online (backlog 3.58): HeartbeatsPerOnlineWindow of its reported
// heartbeat interval, never less than OnlineWindowFloor; nil (never
// reported) is the floor. The SQL that judges online on its own — the
// online period's start in Touch and Heartbeat, the rule evaluation's
// presence read — computes the same window per row from
// host_agents.heartbeat_interval_s with these two constants as its
// parameters.
func OnlineWindow(interval *time.Duration) time.Duration {
	if interval == nil {
		return OnlineWindowFloor
	}
	return max(HeartbeatsPerOnlineWindow*(*interval), OnlineWindowFloor)
}

// isOnline: an agent that never reported is not online, and one whose
// last heartbeat is older than its online window has stopped reporting.
func isOnline(lastSeen *time.Time, interval *time.Duration, now time.Time) bool {
	return lastSeen != nil && now.Sub(*lastSeen) < OnlineWindow(interval)
}

// RecordHeartbeatInterval stores the heartbeat interval the agent reports
// at the start of every stream (the facts message's
// heartbeat_interval_seconds). Zero or less — an agent older than the
// field, or nonsense — stores "not reported", which is the 90 s window, so
// a downgrade reads as before rather than keeping what a newer build said;
// a report over the agent's own maximum is stored as that maximum, so an
// agent cannot widen its own window past 30 minutes, and one under its
// minimum as that minimum.
func (s *Service) RecordHeartbeatInterval(ctx context.Context, serial string, seconds int32) error {
	var interval *time.Duration
	if seconds > 0 {
		d := min(max(time.Duration(seconds)*time.Second, minReportedHeartbeatInterval), maxReportedHeartbeatInterval)
		interval = &d
	}
	return s.store.RecordHeartbeatInterval(ctx, serial, interval)
}
