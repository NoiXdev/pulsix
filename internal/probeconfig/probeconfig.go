// Package probeconfig is the probe assignment set an agent runs for other
// hosts' synthetic checks (backlog 3.13a), as both sides of the agent
// protocol see it: the manager builds it from its probe assignments and
// sends it on a heartbeat's acknowledgement when the agent reports another
// version, and the agent runs it and reports its version back. Both compute
// the version with Version, so the canonical encoding below is the
// contract between every manager and agent build.
package probeconfig

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// Spec is one assigned slot: probe TargetHostID's check CheckKey with
// Plugin and ParamsJSON (canonical JSON, checkplugins.CanonicalParams,
// without the vantage parameters) every IntervalSeconds, against Address.
type Spec struct {
	TargetHostID    uuid.UUID
	CheckKey        string
	Plugin          string
	ParamsJSON      string
	IntervalSeconds int32
	Address         string
}

// sorted returns a copy of specs in canonical order: by target host id
// (canonical string), then check key.
func sorted(specs []Spec) []Spec {
	out := slices.Clone(specs)
	slices.SortFunc(out, func(a, b Spec) int {
		if c := cmp.Compare(a.TargetHostID.String(), b.TargetHostID.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.CheckKey, b.CheckKey)
	})
	return out
}

// encoding renders specs in canonical order as the byte sequence Version
// hashes: per spec its target host id, check key, plugin, params JSON,
// interval seconds (decimal) and address, each followed by a NUL byte.
// Pinned byte for byte by TestVersionCanonicalEncoding; it must never
// change.
func encoding(specs []Spec) []byte {
	var buf bytes.Buffer
	for _, s := range sorted(specs) {
		for _, f := range []string{
			s.TargetHostID.String(), s.CheckKey, s.Plugin, s.ParamsJSON,
			strconv.FormatInt(int64(s.IntervalSeconds), 10), s.Address,
		} {
			buf.WriteString(f)
			buf.WriteByte(0)
		}
	}
	return buf.Bytes()
}

// Version is the hex SHA-256 of specs' canonical encoding; the order of
// specs does not matter, and the empty set has the version of the empty
// encoding.
func Version(specs []Spec) string {
	sum := sha256.Sum256(encoding(specs))
	return hex.EncodeToString(sum[:])
}

// ToProto renders specs as the wire ProbeConfig: the specs in canonical
// order and their Version.
func ToProto(specs []Spec) *agentpb.ProbeConfig {
	ordered := sorted(specs)
	out := make([]*agentpb.ProbeSpec, len(ordered))
	for i, s := range ordered {
		out[i] = &agentpb.ProbeSpec{
			TargetHostId: s.TargetHostID.String(), CheckKey: s.CheckKey, Plugin: s.Plugin,
			ParamsJson: s.ParamsJSON, IntervalSeconds: s.IntervalSeconds, Address: s.Address,
		}
	}
	return &agentpb.ProbeConfig{Version: Version(specs), Probes: out}
}

// FromProto reads a wire ProbeConfig's specs. A target host id that is not
// a canonical lower-case UUID is refused: the manager only ever sends that
// form, and the version hashes it.
func FromProto(cfg *agentpb.ProbeConfig) ([]Spec, error) {
	out := make([]Spec, 0, len(cfg.GetProbes()))
	for _, p := range cfg.GetProbes() {
		id, err := uuid.Parse(p.GetTargetHostId())
		if err != nil || id.String() != p.GetTargetHostId() {
			return nil, fmt.Errorf("probe %q: target host id %q is not a canonical UUID", p.GetCheckKey(), p.GetTargetHostId())
		}
		out = append(out, Spec{
			TargetHostID: id, CheckKey: p.GetCheckKey(), Plugin: p.GetPlugin(),
			ParamsJSON: p.GetParamsJson(), IntervalSeconds: p.GetIntervalSeconds(), Address: p.GetAddress(),
		})
	}
	return out, nil
}

// Bounds of how long an agent takes to run a newly written assignment,
// shared by the agent (which enforces them) and the manager (which waits
// them out before it blames an agent's rounds, internal/probes).
const (
	// MaxHeartbeatInterval is the longest heartbeat interval an agent
	// accepts (PULSIX_HEARTBEAT_INTERVAL is clamped to it). The manager
	// hands out a changed probe config only on a heartbeat.
	MaxHeartbeatInterval = 10 * time.Minute
	// FirstRunWindow caps how long after applying a config an agent's
	// slot runs for the first time (its first run is spread over
	// min(interval, FirstRunWindow)).
	FirstRunWindow = time.Minute
)
