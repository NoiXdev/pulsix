// What a run sends of a template's extra vars (spec 7.75 goal 3): the
// values of active fields only.
package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// sendableVars returns the extra vars a run receives under a template's
// schema: the values of active top-level fields (schema.Sendable). A value
// under an inactive field, or under a name the schema no longer declares,
// stays stored but is not sent. Without a schema every key is sent,
// unchanged. Values are kept as their raw JSON, so a number is sent
// exactly as stored.
func sendableVars(schemaRaw, vars json.RawMessage) (json.RawMessage, error) {
	if isJSONNull(schemaRaw) {
		return vars, nil
	}
	var sc schema.Schema
	if err := json.Unmarshal(schemaRaw, &sc); err != nil {
		return nil, fmt.Errorf("template extra vars schema: %w", err)
	}
	values := map[string]any{}
	if !isJSONNull(vars) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(vars, &raw); err != nil {
			return nil, fmt.Errorf("%w: extra vars are not a JSON object", ErrInvalidInput)
		}
		for k, v := range raw {
			values[k] = v
		}
	}
	return json.Marshal(schema.Sendable(sc.Fields, values))
}

// isJSONNull reports whether raw is empty or the JSON null.
func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}
