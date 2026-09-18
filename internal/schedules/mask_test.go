package schedules_test

import (
	"reflect"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/schedules"
)

func TestMaskSealed(t *testing.T) {
	in := map[string]any{
		"plain":  "v",
		"token":  schema.Sealed([]byte("ct")),
		"nested": map[string]any{"pw": schema.Sealed([]byte("x")), "n": float64(1)},
		"list":   []any{schema.Sealed([]byte("y")), "z"},
	}
	want := map[string]any{
		"plain":  "v",
		"token":  schema.Mask(),
		"nested": map[string]any{"pw": schema.Mask(), "n": float64(1)},
		"list":   []any{schema.Mask(), "z"},
	}
	if got := schedules.MaskSealed(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("MaskSealed = %#v", got)
	}
	if _, sealed := schema.SealedBytes(in["token"]); !sealed {
		t.Fatal("MaskSealed must not modify its input")
	}
	if schedules.MaskSealed(nil) != nil {
		t.Fatal("nil stays nil")
	}
}
