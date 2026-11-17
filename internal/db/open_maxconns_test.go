package db_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestOpenAsRoleWithMaxConns (backlog 3.92): the pool is capped at the
// given size whatever the URL asks for, and its sessions run as the role,
// like OpenAsRole's.
func TestOpenAsRoleWithMaxConns(t *testing.T) {
	ctx := context.Background()
	d, err := db.OpenAsRoleWithMaxConns(ctx, testdb.URL(t)+"&pool_max_conns=20", "pulsix_app", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Pool().Config().MaxConns; got != 3 {
		t.Errorf("MaxConns = %d, want 3", got)
	}
	var role string
	if err := d.Pool().QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "pulsix_app" {
		t.Errorf("current_user = %q, want pulsix_app", role)
	}
}

// TestOpenAsRoleWithMaxConnsRejectsANonPositiveSize: a pool of no
// connections could never serve anything.
func TestOpenAsRoleWithMaxConnsRejectsANonPositiveSize(t *testing.T) {
	if _, err := db.OpenAsRoleWithMaxConns(context.Background(), testdb.URL(t), "pulsix_app", 0); err == nil {
		t.Fatal("want an error for a pool of zero connections")
	}
}
