package render

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// checkTree fails with ErrUnrenderable when a group is not below any root:
// the tree has a cycle (two concurrent moves can leave one behind), and
// Ansible would get groups that hang off nothing.
func (r *renderer) checkTree() error {
	reached := map[uuid.UUID]bool{}
	for _, root := range r.tree.Roots() {
		reached[root] = true
		for _, d := range r.tree.Descendants(root) {
			reached[d] = true
		}
	}
	var detached []string
	for _, g := range r.sortedGroups() {
		if !reached[g.ID] {
			detached = append(detached, g.Name)
		}
	}
	if len(detached) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the group tree has a cycle; inventory groups %s are not below any top-level group, move one of them to a new parent",
		ErrUnrenderable, strings.Join(detached, ", "))
}
