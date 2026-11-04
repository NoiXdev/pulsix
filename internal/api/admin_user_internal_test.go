package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func statusAndDetail(t *testing.T, err error) (int, string) {
	t.Helper()
	var se huma.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("%v is not a huma status error", err)
	}
	var model *huma.ErrorModel
	if !errors.As(err, &model) {
		return se.GetStatus(), ""
	}
	return se.GetStatus(), model.Detail
}

// TestMapTenancyErrLastInstanceAdmin: the deactivate route refuses its
// caller's own account before the last-admin rule, so over HTTP an admin
// meets that rule only when two admins deactivate each other at once
// (tenancy's store test races exactly that). It still answers 409, and the
// self refusal has its own detail.
func TestMapTenancyErrLastInstanceAdmin(t *testing.T) {
	ctx := context.Background()
	if code, _ := statusAndDetail(t, mapTenancyErr(ctx, fmt.Errorf("disable user: %w", tenancy.ErrLastInstanceAdmin))); code != http.StatusConflict {
		t.Fatalf("ErrLastInstanceAdmin: status %d, want 409", code)
	}
	if code, d := statusAndDetail(t, mapTenancyErr(ctx, tenancy.ErrNotOnOwnAccount)); code != http.StatusConflict || d != notOnOwnAccount {
		t.Fatalf("ErrNotOnOwnAccount: status %d detail %q, want 409 %q", code, d, notOnOwnAccount)
	}
}

func TestMapAdminAccountsErr(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err        error
		wantStatus int
		wantDetail string
	}{
		{accounts.ErrForbidden, http.StatusForbidden, ""},
		{accounts.ErrNotOnOwnAccount, http.StatusConflict, notOnOwnAccount},
		{fmt.Errorf("lock: %w", accounts.ErrInvitePending), http.StatusConflict, invitePending},
		{accounts.ErrMailNotConfigured, http.StatusConflict, mailNotConfigured},
		{fmt.Errorf("lock: %w", accounts.ErrNotFound), http.StatusNotFound, ""},
		{accounts.ErrInvalidCursor, http.StatusBadRequest, ""},
		{errors.New("boom"), http.StatusInternalServerError, ""},
	} {
		code, d := statusAndDetail(t, mapAdminAccountsErr(ctx, tc.err))
		if code != tc.wantStatus || (tc.wantDetail != "" && d != tc.wantDetail) {
			t.Errorf("%v: status %d detail %q, want %d %q", tc.err, code, d, tc.wantStatus, tc.wantDetail)
		}
	}
}
