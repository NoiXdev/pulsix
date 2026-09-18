package schedules

import "github.com/noixdev/pulsix/internal/inventory/schema"

// MaskSealed returns a copy of vars with every sealed envelope, at any
// depth, replaced by the {"$secret": "set"} marker reads show.
func MaskSealed(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}
	out, _ := maskValue(vars).(map[string]any)
	return out
}

func maskValue(v any) any {
	if _, sealed := schema.SealedBytes(v); sealed {
		return schema.Mask()
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = maskValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = maskValue(e)
		}
		return out
	}
	return v
}
