package render

import (
	"cmp"
	"slices"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// AssistantValue names one value a rendered inventory carries as plain
// text because an assistant wrote it (decision D6, spec 2026-11-25
// assistant API gaps): a group's own value (Host empty), or a value of
// Host's membership in Group — either a value stored there or a schema
// default of Group filled in. A person saving the value makes it live.
type AssistantValue struct {
	Group string `json:"group"`
	Host  string `json:"host,omitempty"`
	Key   string `json:"key"`
}

// inert is the form an assistant's value reaches Ansible in: every string
// at any depth marked unsafe (ansiblevars.Inert), so Ansible never
// templates, looks up or vault-decrypts it, and every object key Ansible's
// JSON loader would read as a marker of its own dropped first, so the
// markers stay well-formed. The assistant's writes refuse such keys
// already; a schema field a person named so could still put one there.
func inert(v any) any {
	return ansiblevars.Inert(withoutReservedKeys(v))
}

func withoutReservedKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			if !ansiblevars.ReservedKey(k) {
				out[k] = withoutReservedKeys(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = withoutReservedKeys(item)
		}
		return out
	}
	return v
}

func groupMarks(g Group) schema.Marks {
	return schema.Marks{Values: set(g.AssistantKeys), Defaults: set(g.AssistantDefaultKeys)}
}

// membershipMarks: a membership's own marks for its values; its group's
// default marks for the host-scoped defaults that fill in.
func membershipMarks(g Group, m Membership) schema.Marks {
	return schema.Marks{Values: set(m.AssistantKeys), Defaults: set(g.AssistantDefaultKeys)}
}

func set(keys []string) map[string]bool {
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func sortedWritten(w []AssistantValue) []AssistantValue {
	slices.SortFunc(w, func(a, b AssistantValue) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Host, b.Host), cmp.Compare(a.Key, b.Key))
	})
	return w
}
