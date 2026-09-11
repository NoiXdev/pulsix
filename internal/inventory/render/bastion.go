package render

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/sshconf"
)

type bastionCandidate struct {
	bastion uuid.UUID
	group   Group
	depth   int
}

// resolveBastion applies spec §5's order: the host's own setting, then the
// nearest group jump host (deepest defining group wins, ties by group name),
// then the zone default, then none.
func (r *renderer) resolveBastion(h Host, ssh sshconf.HostSSH, ms []Membership) ([]sshconf.Bastion, string, error) {
	switch ssh.BastionMode {
	case sshconf.ModeNone:
		return nil, SourceNone, nil
	case sshconf.ModeExplicit:
		if ssh.BastionID == nil {
			return nil, "", errors.New("explicit jump host mode without a jump host")
		}
		hops, err := sshconf.Chain(r.in.Bastions, *ssh.BastionID)
		return hops, SourceHost, err
	}
	var cands []bastionCandidate
	for _, m := range ms {
		for _, gid := range append([]uuid.UUID{m.GroupID}, r.tree.Ancestors(m.GroupID)...) {
			if g := r.groups[gid]; g.BastionID != nil {
				cands = append(cands, bastionCandidate{bastion: *g.BastionID, group: g, depth: r.tree.Depth(gid)})
				break
			}
		}
	}
	if len(cands) > 0 {
		slices.SortFunc(cands, func(a, b bastionCandidate) int {
			return cmp.Or(cmp.Compare(b.depth, a.depth), cmp.Compare(a.group.Name, b.group.Name))
		})
		win := cands[0]
		for _, c := range cands[1:] {
			if c.depth == win.depth && c.bastion != win.bastion {
				r.warnings = append(r.warnings, Warning{Host: h.Name, Group: c.group.Name,
					Message: fmt.Sprintf("groups %s and %s set different jump hosts; %s wins", win.group.Name, c.group.Name, win.group.Name)})
			}
		}
		hops, err := sshconf.Chain(r.in.Bastions, win.bastion)
		return hops, "group:" + win.group.Name, err
	}
	if b, ok := r.in.ZoneBastions[h.ZoneID]; ok {
		hops, err := sshconf.Chain(r.in.Bastions, b)
		return hops, SourceZone, err
	}
	return nil, SourceNone, nil
}
