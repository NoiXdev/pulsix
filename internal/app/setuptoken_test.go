package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/setup"
)

type fakeSetupStatus struct {
	st  setup.Status
	err error
}

func (f fakeSetupStatus) Status(context.Context) (setup.Status, error) { return f.st, f.err }

func TestLogSetupTokenOnlyWhileSetupIsNotCompleted(t *testing.T) {
	const token = "ABCD-EFGH-IJKL-MNOP"
	for _, tc := range []struct {
		name     string
		reader   fakeSetupStatus
		wantLine bool
	}{
		{"fresh instance", fakeSetupStatus{}, true},
		{"completed", fakeSetupStatus{st: setup.Status{Completed: true}}, false},
		{"status unreadable", fakeSetupStatus{err: errors.New("db down")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			logSetupToken(context.Background(), log, tc.reader, token, "https://pulsix.example.com/")
			out := buf.String()
			has := strings.Contains(out, token)
			if has != tc.wantLine {
				t.Fatalf("token logged = %v, want %v; log %q", has, tc.wantLine, out)
			}
			if !tc.wantLine {
				return
			}
			for _, want := range []string{"level=WARN", "setup token", "https://pulsix.example.com/setup?token=" + token} {
				if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
					t.Errorf("log %q lacks %q", out, want)
				}
			}
		})
	}
}

func TestRunSetupTokenPrintsTokenAndLink(t *testing.T) {
	cfg := testConfig(t)
	want := setup.Token(cfg.MasterKey)

	// Without a database the token is still printed: it needs only the
	// master key and the public URL.
	noDB := cfg
	noDB.DatabaseURL = ""
	var out bytes.Buffer
	if err := RunSetupToken(context.Background(), noDB, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{want, "http://localhost:8080/setup?token=" + want} {
		if !strings.Contains(out.String(), s) {
			t.Fatalf("setup-token without a database printed %q, want %q in it", out.String(), s)
		}
	}

	// A reachable database whose setup is not completed: the same.
	out.Reset()
	if err := RunSetupToken(context.Background(), cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), want) {
		t.Fatalf("setup-token on a fresh instance printed %q", out.String())
	}
}

func TestRunSetupTokenOnACompletedInstance(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	completeSetupThroughHandler(t, a, cfg, "token-cmd-admin@example.test", "token-cmd")

	var out bytes.Buffer
	if err := RunSetupToken(ctx, cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "setup is already completed; no token is needed") {
		t.Fatalf("setup-token on a completed instance printed %q", out.String())
	}
	if strings.Contains(out.String(), setup.Token(cfg.MasterKey)) {
		t.Fatalf("setup-token printed the token on a completed instance: %q", out.String())
	}
}

// completeSetupThroughHandler runs the wizard against a's handler with the
// setup token of cfg's master key.
func completeSetupThroughHandler(t *testing.T, a *App, cfg config.Config, email, org string) {
	t.Helper()
	body := `{"admin_email":"` + email + `","admin_password":"correct horse battery","admin_name":"Root","org_slug":"` + org +
		`","org_name":"Org","setup_token":"` + setup.Token(cfg.MasterKey) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete setup: status %d, body %s", rec.Code, rec.Body.String())
	}
}
