package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// TestMustChangeGates pins the two gates against every principal shape:
// requireMFAComplete holds back a must-change cookie session but not a
// token, and requirePasswordChangeSession — POST /auth/password's gate —
// admits exactly the MFA-complete cookie sessions, forced change or not.
func TestMustChangeGates(t *testing.T) {
	tokenID := uuid.Must(uuid.NewV7())
	uid := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name             string
		principal        *auth.Principal
		wantMFAComplete  int // 0 = admitted
		wantPasswordGate int
	}{
		{"no principal", nil, http.StatusUnauthorized, http.StatusUnauthorized},
		{"pending session", &auth.Principal{UserID: uid}, http.StatusForbidden, http.StatusForbidden},
		{"complete session", &auth.Principal{UserID: uid, MFAComplete: true}, 0, 0},
		{"must-change session", &auth.Principal{UserID: uid, MFAComplete: true, MustChangePassword: true}, http.StatusForbidden, 0},
		{"must-change pending session", &auth.Principal{UserID: uid, MustChangePassword: true}, http.StatusForbidden, http.StatusForbidden},
		{"token", &auth.Principal{UserID: uid, MFAComplete: true, TokenID: &tokenID}, 0, http.StatusForbidden},
		{"token of a must-change account", &auth.Principal{UserID: uid, MFAComplete: true, TokenID: &tokenID, MustChangePassword: true}, 0, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.principal != nil {
				ctx = auth.WithPrincipal(ctx, *tc.principal)
			}
			_, err := requireMFAComplete(ctx)
			if got := gateStatus(err); got != tc.wantMFAComplete {
				t.Errorf("requireMFAComplete: status %d (%v), want %d", got, err, tc.wantMFAComplete)
			}
			_, err = requirePasswordChangeSession(ctx)
			if got := gateStatus(err); got != tc.wantPasswordGate {
				t.Errorf("requirePasswordChangeSession: status %d (%v), want %d", got, err, tc.wantPasswordGate)
			}
		})
	}
}

func gateStatus(err error) int {
	if err == nil {
		return 0
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return se.GetStatus()
	}
	return -1
}
