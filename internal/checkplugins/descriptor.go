// Package checkplugins describes every check plugin in one place: its name,
// where it runs, the parameters it accepts and how each is checked, the
// list parameter that enumerates its items, and the metrics it reports
// (backlog 3.54, spec docs/superpowers/specs/2026-11-12-plugin-descriptors-design.md).
//
// It is a leaf package — it imports nothing from the rest of Pulsix — so the
// manager's validation (internal/checks), the rules catalogue
// (internal/rules), the API and, through the API, the SPA can all derive
// from the same descriptors. The agent's plugins are not generated from
// them; a test keeps the agent's registry and the descriptors with
// Runs = Agent in step.
//
// One file per plugin holds its descriptor; registry.go's constructor list
// registers them in the order the SPA shows them.
//
// Adding a plugin: add its descriptor file here and one entry to that list;
// the other steps (agent package, golden tests, SPA fixture and schema,
// i18n, probe case, docs) are listed in the "Adding a plugin" note of
// internal/agent/checks/plugins.
package checkplugins

import "slices"

// Runs says where a plugin's check is executed.
type Runs string

const (
	// Agent plugins run on the host's agent, which receives the definition.
	Agent Runs = "agent"
	// Server plugins (the synthetic probes, backlog 3.13) run on the Pulsix
	// server and are never pushed to an agent.
	Server Runs = "server"
)

// ParamType is the JSON type a parameter's value has.
type ParamType string

const (
	// TypeString is a JSON string.
	TypeString ParamType = "string"
	// TypeInteger is a whole JSON number (a JSON body decodes it as a
	// float64 with no fraction; a Go caller may pass an int or int64).
	TypeInteger ParamType = "integer"
	// TypeBool is true or false.
	TypeBool ParamType = "bool"
	// TypeEnum is a JSON string that is exactly one of Options, case included.
	TypeEnum ParamType = "enum"
	// TypeList is a JSON array of strings. A list never accepts an empty
	// ("") entry.
	TypeList ParamType = "list"
)

// MetricKind says what a metric's samples carry.
type MetricKind string

const (
	// Numeric metrics carry a number a threshold can compare against.
	Numeric MetricKind = "numeric"
	// Discrete metrics carry a label a set of states compares against.
	Discrete MetricKind = "discrete"
)

// ParamSpec describes one parameter. Every parameter is optional unless
// Required is set; a parameter a definition leaves out is applied with its
// default by whoever runs the check, never filled in by the manager.
type ParamSpec struct {
	Name string    `json:"name"`
	Type ParamType `json:"type"`
	// Required parameters must be present.
	Required bool `json:"required,omitempty"`
	// Default is what the plugin applies when the parameter is absent —
	// documentation only (the SPA shows it as a placeholder or a
	// preselection); nil when there is no single default.
	Default any `json:"default,omitempty"`
	// Example is a sample value for a parameter without a default, for the
	// SPA to show while the field is empty.
	Example string `json:"example,omitempty"`
	// Min and Max bound an integer, both inclusive.
	Min *int64 `json:"min,omitempty"`
	Max *int64 `json:"max,omitempty"`
	// MaxEntries bounds a list's length; 0 means unbounded.
	MaxEntries int `json:"max_entries,omitempty"`
	// MaxBytes bounds a string, or each entry of a list, in bytes; 0 means
	// unbounded.
	MaxBytes int `json:"max_bytes,omitempty"`
	// NonEmpty refuses the empty string for a string, and the empty list for
	// a list.
	NonEmpty bool `json:"non_empty,omitempty"`
	// Options are an enum's allowed values, in the order a form offers them.
	Options []string `json:"options,omitempty"`
	// Format names a validator (formats.go) a string, or each entry of a
	// list, must pass after the checks above.
	Format Format `json:"format,omitempty"`
}

// MetricSpec is one metric a plugin reports. A rule names it; the check key
// it belongs to is the plugin's name.
type MetricSpec struct {
	Name string     `json:"name"`
	Kind MetricKind `json:"kind"`
}

// Descriptor is everything the manager and the SPA know about one plugin.
// It marshals to JSON without its hooks.
type Descriptor struct {
	Name string `json:"name"`
	Runs Runs   `json:"runs"`
	// Params in the order a form shows them. A plugin without parameters
	// refuses every key.
	Params []ParamSpec `json:"params"`
	// ItemParam names the list parameter that enumerates the plugin's items
	// (disk's mounts, …); "" when its items are not enumerable in its
	// parameters. See AsksForItem.
	ItemParam string `json:"item_param,omitempty"`
	// HostItemAlwaysAsked marks a plugin that also reports on the whole-host
	// item "" whatever its ItemParam list says (the hardware plugins).
	HostItemAlwaysAsked bool `json:"host_item_always_asked,omitempty"`
	// Metrics the plugin reports.
	Metrics []MetricSpec `json:"metrics"`

	// ItemEntry maps a reported item to the ItemParam entry that asks for
	// it, for a plugin whose items are not always the entries themselves
	// (smart's "<path> [<type>]"); nil means the item is the entry.
	ItemEntry func(item string) string `json:"-"`
	// ItemAsked replaces AsksForItem's comparison of item with the
	// ItemParam entries, for a plugin whose entries are not its items'
	// names or whose other parameters widen what it asks for (snmp: an
	// entry "name=OID" asks for item "name", and interfaces asks for any
	// item the device names). It runs only where the generic rules have
	// not answered already: item is not a HostItemAlwaysAsked plugin's "",
	// and the list is absent or a list of strings — empty included, so the
	// hook says what no list means (http: no JSON item at all). entries is
	// the list as strings, nil when absent. Nil means the item must be one
	// of the entries (after ItemEntry), or anything when the list is absent
	// or empty; a descriptor sets at most one of the two.
	ItemAsked func(params map[string]any, entries []string, item string) bool `json:"-"`
	// MetricAsked reports whether a check with params still asks for metric
	// (backlog 3.88), for a plugin whose parameters decide whether a metric
	// is reported at all (http: http.body_match only with body_contains).
	// It is consulted for every item, the whole-host item "" included. Nil
	// means every metric is asked. See AsksForMetric.
	MetricAsked func(params map[string]any, metric string) bool `json:"-"`
	// HeldWhile is for a plugin whose item can enter a state in which the
	// agent reports only the metric carrying that state (backlog 3.74, 3.9b):
	// zfs reports only zfs.state for an EXPORTED or MISSING pool, smart
	// only smart.power_mode for a disk it leaves asleep. While the item's
	// current reading of HeldWhile.Metric is one of HeldWhile.Labels, its
	// other metrics are held (HeldByItemState): not asked, and their last
	// state and alert wait unchanged for the item to leave that state,
	// instead of turning stale. The state's own metric is always asked,
	// or the item could never leave it. It is declared as data, not as a
	// hook, because the metric expiry applies it in SQL (HeldStates). Nil
	// means the item's state never matters.
	HeldWhile *HeldState `json:"-"`
	// Check validates what spans parameters or needs code beyond ParamSpec
	// and the formats. It runs after every parameter passed its own spec
	// and returns a description of the problem, which Validate prefixes
	// with the plugin's name. Only snmp has one today.
	Check func(params map[string]any) error `json:"-"`
}

// HeldState names the metric that carries an item's state and the labels
// of it in which the item's other metrics are held (Descriptor.HeldWhile).
// The metric is one of the plugin's own, so it names the plugin too.
type HeldState struct {
	Metric string
	Labels []string
}

// holds reports whether metric of an item whose current state is labels
// is held: the item reads one of the holding labels, and metric is not
// the one that carries it. Nil labels hold nothing.
func (h *HeldState) holds(labels map[string]string, metric string) bool {
	if h == nil || metric == h.Metric {
		return false
	}
	label, ok := labels[h.Metric]
	return ok && slices.Contains(h.Labels, label)
}

// bound is a pointer to n, for ParamSpec.Min and Max.
func bound(n int64) *int64 { return &n }
