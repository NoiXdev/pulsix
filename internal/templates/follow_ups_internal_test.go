package templates

import (
	"testing"

	"github.com/google/uuid"
)

func TestFindCycle(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name  string
		edges map[uuid.UUID][]uuid.UUID
		want  []uuid.UUID
	}{
		{"none", map[uuid.UUID][]uuid.UUID{a: {b}}, nil},
		{"direct", map[uuid.UUID][]uuid.UUID{a: {b}, b: {a}}, []uuid.UUID{a, b, a}},
		{"indirect", map[uuid.UUID][]uuid.UUID{a: {b}, b: {c}, c: {a}}, []uuid.UUID{a, b, c, a}},
		{"diamond", map[uuid.UUID][]uuid.UUID{a: {b, c}, b: {d}, c: {d}}, nil},
		{"cycle elsewhere is not through from", map[uuid.UUID][]uuid.UUID{a: {b}, b: {c}, c: {b}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findCycle(tc.edges, a)
			if len(got) != len(tc.want) {
				t.Fatalf("FindCycle = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("FindCycle = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
