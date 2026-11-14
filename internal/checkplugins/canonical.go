package checkplugins

import "encoding/json"

// CanonicalParams renders a check's params as canonical JSON: "{}" for nil
// or empty (never the bare "null" json.Marshal gives a nil map, so two
// checks that both take no parameters encode alike whether the map was
// nil or merely empty), sorted-key JSON otherwise — encoding/json sorts
// map keys at every nesting level, so marshalling map[string]any is
// already canonical. The manager hashes it into a check set's version and
// the agent reports its local checks' params in it, so both sides encode
// through this one function.
func CanonicalParams(params map[string]any) ([]byte, error) {
	if len(params) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(params)
}
