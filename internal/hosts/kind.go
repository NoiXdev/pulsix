package hosts

import "fmt"

// Kind says whether a host is a server or a client (backlog 3.57, spec
// docs/superpowers/specs/2026-11-13-client-hosts-design.md). A server is
// expected to be on: an agent that stops reporting is a problem. A client
// (a notebook, a desktop) is off by design: while its agent is not online
// the evaluation freezes the host instead of turning it unknown. The
// values are the hosts.kind column's (migration 00090), whose CHECK
// admits exactly these two.
type Kind string

const (
	// KindServer is the default: every host created without a kind, and
	// every host that existed before kinds did.
	KindServer Kind = "server"
	// KindClient is a host that may be off without that being a problem.
	KindClient Kind = "client"
)

// validateKind accepts the two kinds and the empty kind
// ("not given": create makes a server, update keeps the stored kind).
func validateKind(k Kind) error {
	switch k {
	case "", KindServer, KindClient:
		return nil
	default:
		return fmt.Errorf("%w: host kind must be server or client", ErrInvalidInput)
	}
}

// kindParam is the nullable query parameter for k: nil when no kind is
// given, which the queries read as the default (create) or "keep" (update).
func kindParam(k Kind) *string {
	if k == "" {
		return nil
	}
	s := string(k)
	return &s
}
