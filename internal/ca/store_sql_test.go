package ca_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestSQLStoreRoundTrip follows the credentials store test: Load misses
// before the first Save, the stored bytes survive unchanged, and Save
// replaces them.
func TestSQLStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	store := ca.NewSQLStore(d)

	sealed, found, err := store.Load(t.Context())
	if err != nil || found || sealed != nil {
		t.Fatalf("Load before Save = %q, %v, %v, want nothing found", sealed, found, err)
	}

	if err := store.Save(t.Context(), []byte("sealed-1")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sealed, found, err = store.Load(t.Context())
	if err != nil || !found || string(sealed) != "sealed-1" {
		t.Fatalf("Load = %q, %v, %v", sealed, found, err)
	}

	if err := store.Save(t.Context(), []byte("sealed-2")); err != nil {
		t.Fatalf("Save (replace): %v", err)
	}
	sealed, found, err = store.Load(t.Context())
	if err != nil || !found || string(sealed) != "sealed-2" {
		t.Fatalf("Load after replace = %q, %v, %v", sealed, found, err)
	}
}

// TestSQLStorePropagatesErrors checks that failures other than an absent
// row surface as errors, not as a silent "not found".
func TestSQLStorePropagatesErrors(t *testing.T) {
	d := testdb.Open(t)
	store := ca.NewSQLStore(d)
	d.Close()

	if sealed, found, err := store.Load(context.Background()); err == nil || found {
		t.Fatalf("Load on a closed pool = %q, %v, %v, want an error", sealed, found, err)
	}
	if err := store.Save(context.Background(), []byte("x")); err == nil {
		t.Fatal("Save on a closed pool must fail")
	}
}
