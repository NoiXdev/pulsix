package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/auth"
)

// TestMapTokenErr exercises mapTokenErr's one override on top of mapAuthErr
// directly, without spinning up a server: auth.ErrNotFound must become 404
// (an unknown token id, or one belonging to a different org — see
// registerTokenRoutes' Delete handler), while anything mapAuthErr itself
// doesn't specifically recognize must fall back to a generic 500 and never
// leak the underlying error text to a client. End-to-end authorization for
// these routes (org membership, role, bearer-token scope capping) is
// covered by internal/api/token_routes_test.go, which exercises them
// through the real, mounted router instead of calling this unexported
// function directly.
func TestMapTokenErr(t *testing.T) {
	ctx := context.Background()

	err := mapTokenErr(ctx, auth.ErrNotFound)
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != 404 {
		t.Fatalf("mapTokenErr(ErrNotFound) = %v, want 404", err)
	}

	unmapped := errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
	err = mapTokenErr(ctx, unmapped)
	if !errors.As(err, &se) || se.GetStatus() != 500 {
		t.Fatalf("mapTokenErr(unmapped) = %v, want 500", err)
	}
	if errors.Is(err, unmapped) || strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("mapTokenErr leaked internal detail: %v", err)
	}
}
