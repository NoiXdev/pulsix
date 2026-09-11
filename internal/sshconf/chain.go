package sshconf

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Chain returns the hops needed to reach bastion id, first hop first: a
// bastion reached through another lists that one before itself. It fails on
// an unknown id, a cycle, or a chain longer than MaxChain.
func Chain(all map[uuid.UUID]Bastion, id uuid.UUID) ([]Bastion, error) {
	hops, problem := chain(all, id)
	if problem != "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInput, problem)
	}
	return hops, nil
}

// chain is Chain with the reason there is no valid chain as plain text.
func chain(all map[uuid.UUID]Bastion, id uuid.UUID) ([]Bastion, string) {
	var rev []Bastion
	seen := make(map[uuid.UUID]bool)
	cur := id
	for {
		if seen[cur] {
			return nil, "the jump host chain contains a cycle"
		}
		seen[cur] = true
		b, ok := all[cur]
		if !ok {
			return nil, fmt.Sprintf("jump host %s does not exist", cur)
		}
		rev = append(rev, b)
		if len(rev) > MaxChain {
			return nil, fmt.Sprintf("jump host chains are limited to %d hops", MaxChain)
		}
		if b.ViaBastionID == nil {
			break
		}
		cur = *b.ViaBastionID
	}
	slices.Reverse(rev)
	return rev, ""
}

// ProxyJump renders hops as the value of ssh's ProxyJump option
// (user@host:port, comma separated, first hop first). IPv6 addresses are
// bracketed.
func ProxyJump(hops []Bastion) string {
	parts := make([]string, len(hops))
	for i, h := range hops {
		addr := h.Address
		if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
			addr = "[" + addr + "]"
		}
		parts[i] = h.Username + "@" + addr + ":" + strconv.Itoa(h.Port)
	}
	return strings.Join(parts, ",")
}
