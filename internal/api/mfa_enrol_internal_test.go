package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/auth"
)

// TestEnrolRefusalForAnEndedSessionMapsTo401 pins the seam the enrol handler
// maps BeginTOTP's refusal through (backlog 7.119). The auth package tests
// prove Enrolment.BeginTOTP returns auth.ErrUnauthenticated, wrapped by its
// sessionEnded helper, when the session ended before its secret is written;
// the handler passes that error to mapAuthErr, which must answer 401 and
// never 500 or 403.
func TestEnrolRefusalForAnEndedSessionMapsTo401(t *testing.T) {
	err := fmt.Errorf("begin TOTP enrolment: the session has ended: %w", auth.ErrUnauthenticated)
	status, _ := statusAndDetail(t, mapAuthErr(context.Background(), err))
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}
