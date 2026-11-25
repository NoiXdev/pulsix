package render

import "fmt"

// SetHostVar sets one variable in host's own variables (_meta.hostvars) of
// the rendered document, replacing any value a group or membership gave
// the host under that name; an inventory host variable beats every group
// variable, so nothing in the inventory can shadow it. It fails when the
// document carries no such host. The claim sets the per-host enrollment
// token this way (spec 2026-11-25 assistant API gaps, B).
func (inv *Inventory) SetHostVar(host, name string, value any) error {
	meta, ok := inv.Doc["_meta"].(map[string]any)
	if !ok {
		return fmt.Errorf("inventory document has no _meta")
	}
	hostvars, ok := meta["hostvars"].(map[string]any)
	if !ok {
		return fmt.Errorf("inventory document has no hostvars")
	}
	vars, ok := hostvars[host].(map[string]any)
	if !ok {
		return fmt.Errorf("inventory document has no host %s", host)
	}
	vars[name] = value
	return nil
}
