package checkplugins

import "time"

// The bounds of a check's interval. They live in this leaf package so the
// manager (which validates check definitions) and the agent (which
// validates the local checks in its agent.yaml) read one definition.
const (
	// MinInterval is the shortest check interval. It is also the fastest
	// an agent can legitimately produce observations, which
	// internal/agentapi's per-stream ingest budget uses as its refill
	// period (backlog 7.111).
	MinInterval = 10 * time.Second
	// MaxInterval is the longest check interval. internal/config derives
	// the shortest item expiry period from it (backlog 3.12b): an item must
	// never be forgotten between two reports of its check.
	MaxInterval = 24 * time.Hour
)
