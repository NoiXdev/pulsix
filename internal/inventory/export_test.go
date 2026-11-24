package inventory

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// OpeningEveryRow returns a copy of r that opens the sealed values of
// every group and membership of the organization, whatever the scope: the
// oracle the selective opening is checked against.
func OpeningEveryRow(r *Renderer) *Renderer {
	c := *r
	c.carries = func(in render.Input) (render.Carried, error) {
		all := render.Carried{Groups: map[uuid.UUID]bool{}, Memberships: map[render.MembershipKey]bool{}}
		for _, g := range in.Groups {
			all.Groups[g.ID] = true
		}
		for _, m := range in.Memberships {
			all.Memberships[render.MembershipKey{GroupID: m.GroupID, HostID: m.HostID}] = true
		}
		return all, nil
	}
	return &c
}
