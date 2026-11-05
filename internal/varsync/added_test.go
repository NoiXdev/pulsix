package varsync_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsync"
)

// TestComputeHoldsAnAdditionOverAKeptValue: a field the source declares
// anew over a value the template kept from a field a person removed
// (spec 7.75 owner decision 4) is added only when the kept value fits it.
// A sealed secret kept under a field that is no longer secret, or a value
// that does not fit, is held for a person in automatic mode: never added
// active silently, so a run never receives the secret as a plain value.
func TestComputeHoldsAnAdditionOverAKeptValue(t *testing.T) {
	sealed := schema.Sealed([]byte("ciphertext"))
	required := func(f *schema.Field) { f.Required = true }
	object := marker("db", schema.TypeObject, 5)
	object.Fields = []schema.Field{{Name: "user", Type: schema.TypeString}}
	cases := []struct {
		name string
		cand schema.Field
		kept any
		want varsync.ReviewReason // "" means no review
	}{
		{"sealed secret under a string", marker("token", schema.TypeString, 3, required), sealed, varsync.ReasonSecret},
		{"sealed secret under an optional string", marker("token", schema.TypeString, 3), sealed, varsync.ReasonSecret},
		{"sealed secret under an object", object, sealed, varsync.ReasonSecret},
		{"sealed secret inside an object", object, map[string]any{"user": sealed}, varsync.ReasonSecret},
		{"sealed secret under a secret", marker("token", schema.TypeSecret, 3, required), sealed, ""},
		{"a string under an int", marker("port", schema.TypeInt, 3), "eighty", varsync.ReasonValue},
		{"a fitting value", marker("port", schema.TypeInt, 3), 80.0, ""},
		{"nothing kept", marker("token", schema.TypeString, 3, required), nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]any{"other": "x"}
			if tc.kept != nil {
				stored[tc.cand.Name] = tc.kept
			}
			p := varsync.Compute(nil, source(tc.cand), stored)
			if len(p.Added) != 1 {
				t.Fatalf("added = %s", jsonOf(t, p.Added))
			}
			a := p.Added[0]
			if a.NeedsReview != (tc.want != "") || a.Reason != tc.want {
				t.Fatalf("needs review %v reason %q, want %q", a.NeedsReview, a.Reason, tc.want)
			}
			auto := varsync.Apply(nil, p, varsync.ApplyOptions{Automatic: true})
			if held := tc.want != ""; held != (len(auto) == 0) {
				t.Errorf("automatic apply = %s, held %v", jsonOf(t, auto), held)
			}
			if person := varsync.Apply(nil, p, varsync.ApplyOptions{}); len(person) != 1 {
				t.Errorf("a person's apply = %s, want the field added", jsonOf(t, person))
			}
		})
	}
}
