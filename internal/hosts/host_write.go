package hosts

import (
	"fmt"

	"github.com/google/uuid"
)

// Jump host modes, the values of host_ssh.bastion_mode. They mirror
// sshconf.BastionMode, which owns the full SSH settings; package hosts
// cannot import sshconf (sshconf imports hosts), so the three names are
// repeated here.
const (
	// JumpModeInherit takes the jump host from the inventory group or the
	// zone. It is also what a host without SSH settings does.
	JumpModeInherit = "inherit"
	// JumpModeNone connects directly, even when a group or the zone names
	// a jump host.
	JumpModeNone = "none"
	// JumpModeExplicit connects through BastionID.
	JumpModeExplicit = "explicit"
)

// Jump is a host's jump-host choice; Mode is "inherit", "none" or "explicit".
type Jump struct {
	Mode      string
	BastionID *uuid.UUID // set iff Mode == "explicit"
}

// HostExtras are the parts of one host write that are stored outside the
// hosts row. The store writes them in the same transaction as the row, so
// a failure in any part writes nothing. A nil field leaves that part as it
// is.
type HostExtras struct {
	// Jump sets only bastion_mode and bastion_id of the host's SSH
	// settings; user, port, credential and host-key pin stay.
	Jump *Jump
	// Tags replaces the host's manual tags (fact tags stay). Every element
	// carries Source == TagSourceManual. A non-nil empty slice clears them.
	Tags *[]Tag
}

// validateJump reports ErrInvalidInput for a mode the schema does not know
// or a BastionID that does not agree with the mode. Whether the jump host
// exists in the organization is the store's answer (ErrNotFound), from the
// composite foreign key, inside the write's own transaction.
func validateJump(j Jump) error {
	switch j.Mode {
	case JumpModeInherit, JumpModeNone, JumpModeExplicit:
	default:
		return fmt.Errorf("%w: jump host mode must be inherit, none or explicit", ErrInvalidInput)
	}
	if (j.Mode == JumpModeExplicit) != (j.BastionID != nil) {
		return fmt.Errorf("%w: a jump host is named exactly when the mode is explicit", ErrInvalidInput)
	}
	return nil
}

// extrasFrom validates in's jump host and tags and returns them in the
// shape the store writes, every tag marked manual.
func extrasFrom(in HostInput) (HostExtras, error) {
	var x HostExtras
	if in.Jump != nil {
		if err := validateJump(*in.Jump); err != nil {
			return HostExtras{}, err
		}
		j := *in.Jump
		x.Jump = &j
	}
	if in.Tags != nil {
		if err := validateTags(*in.Tags); err != nil {
			return HostExtras{}, err
		}
		manual := make([]Tag, len(*in.Tags))
		for i, t := range *in.Tags {
			manual[i] = Tag{Key: t.Key, Value: t.Value, Source: TagSourceManual}
		}
		x.Tags = &manual
	}
	return x, nil
}
