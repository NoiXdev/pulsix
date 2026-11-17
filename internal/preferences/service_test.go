package preferences_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/preferences"
)

type fakeStore struct {
	rows map[uuid.UUID]preferences.Prefs
}

func newFake() *fakeStore { return &fakeStore{rows: map[uuid.UUID]preferences.Prefs{}} }

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (preferences.Prefs, error) {
	p, ok := f.rows[id]
	if !ok {
		return preferences.Prefs{}, preferences.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) Put(_ context.Context, id uuid.UUID, p preferences.Prefs) (preferences.Prefs, error) {
	f.rows[id] = p
	return p, nil
}

func TestGetReturnsDefaultsWithoutRow(t *testing.T) {
	svc := preferences.NewService(newFake())
	p, err := svc.Get(t.Context(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if p.Density != "standard" || p.PageSize != 25 || p.Lists == nil || len(p.Lists) != 0 {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestSetValidates(t *testing.T) {
	cols := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("c%d", i)
		}
		return out
	}
	manyLists := map[string]preferences.List{}
	for i := 0; i < 65; i++ {
		manyLists[fmt.Sprintf("l%d", i)] = preferences.List{Columns: []string{"name"}}
	}
	// 40 lists of 40 columns of 16 chars: well over 16 KiB, within every count limit.
	bigLists := map[string]preferences.List{}
	for i := 0; i < 40; i++ {
		c := make([]string, 40)
		for j := range c {
			c[j] = fmt.Sprintf("c%s%d_%d", strings.Repeat("x", 10), i, j)
		}
		bigLists[fmt.Sprintf("l%d", i)] = preferences.List{Columns: c}
	}
	one := func(id string, c ...string) map[string]preferences.List {
		return map[string]preferences.List{id: {Columns: c}}
	}

	invalid := []struct {
		name string
		p    preferences.Prefs
	}{
		{"density", preferences.Prefs{Density: "dense", PageSize: 25}},
		{"page size", preferences.Prefs{Density: "standard", PageSize: 20}},
		{"upper-case list id", preferences.Prefs{Density: "standard", PageSize: 25, Lists: one("Hosts", "name")}},
		{"65-char list id", preferences.Prefs{Density: "standard", PageSize: 25, Lists: one("a"+strings.Repeat("b", 64), "name")}},
		{"column id starts with digit", preferences.Prefs{Density: "standard", PageSize: 25, Lists: one("hosts", "1col")}},
		{"65 lists", preferences.Prefs{Density: "standard", PageSize: 25, Lists: manyLists}},
		{"41 columns", preferences.Prefs{Density: "standard", PageSize: 25, Lists: map[string]preferences.List{"hosts": {Columns: cols(41)}}}},
		{"repeated column", preferences.Prefs{Density: "standard", PageSize: 25, Lists: one("hosts", "name", "zone", "name")}},
		{"document over 16 KiB", preferences.Prefs{Density: "standard", PageSize: 25, Lists: bigLists}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			_, err := preferences.NewService(f).Set(t.Context(), uuid.New(), tc.p)
			if !errors.Is(err, preferences.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if len(f.rows) != 0 {
				t.Fatal("an invalid document was stored")
			}
		})
	}

	valid := preferences.Prefs{Density: "compact", PageSize: 100, Lists: map[string]preferences.List{
		"hosts":  {Columns: []string{"name", "zone", "active_runs"}},
		"agents": {Columns: []string{"agent.version", "last_seen"}},
	}}
	got, err := preferences.NewService(newFake()).Set(t.Context(), uuid.New(), valid)
	if err != nil {
		t.Fatalf("valid document: %v", err)
	}
	if got.Density != "compact" || got.PageSize != 100 || len(got.Lists) != 2 {
		t.Fatalf("answer = %+v", got)
	}
}

func TestSetNormalisesNilLists(t *testing.T) {
	got, err := preferences.NewService(newFake()).Set(t.Context(), uuid.New(), preferences.Prefs{Density: "standard", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lists == nil {
		t.Fatal("Lists is nil, want an empty map")
	}
}
