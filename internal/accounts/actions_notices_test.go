package accounts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/mail"
)

// TestNoticeFailureLogsThroughTheServiceLogger: the line for a notice that
// could not be mailed goes to the logger the service was built with, not to
// the process-wide default.
func TestNoticeFailureLogsThroughTheServiceLogger(t *testing.T) {
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := &fakeStore{
		accounts:  map[uuid.UUID]bool{target: true},
		recipient: accounts.Recipient{Email: "target@example.test"},
	}
	mailer := &fakeMailer{sendErr: errors.New("smtp down")}
	var logs bytes.Buffer
	svc := accounts.NewService(store, mailer, "https://pulsix.example", func() time.Time { return testNow }, &cyclingReader{},
		accounts.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	_, notice, err := svc.SetTemporaryPassword(context.Background(), true, actor, target)
	if err != nil || notice != accounts.NoticeFailed {
		t.Fatalf("notice %q, err %v; want failed", notice, err)
	}
	out := logs.String()
	if !strings.Contains(out, "account notice not sent") || !strings.Contains(out, "smtp down") {
		t.Fatalf("log %q; want the failed-notice line with the cause", out)
	}
	if strings.Contains(out, "target@example.test") {
		t.Fatalf("log %q names the address", out)
	}
}

// TestNoticesSkipDeactivatedAccounts: an action on a deactivated account
// still happens, but the person is not mailed about it (NoticeSkipped).
func TestNoticesSkipDeactivatedAccounts(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	newStore := func() *fakeStore {
		return &fakeStore{
			accounts:    map[uuid.UUID]bool{target: true},
			deactivated: map[uuid.UUID]bool{target: true},
			recipient:   accounts.Recipient{Email: "target@example.test"},
		}
	}

	store, mailer := newStore(), &fakeMailer{}
	_, notice, err := actionService(store, mailer).SetTemporaryPassword(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if notice != accounts.NoticeSkipped || len(mailer.sent) != 0 || len(store.tempCalls) != 1 {
		t.Fatalf("temporary password: notice %q, mails %d, password set %d times; want skipped, none, once", notice, len(mailer.sent), len(store.tempCalls))
	}

	store, mailer = newStore(), &fakeMailer{}
	notice, err = actionService(store, mailer).ResetSecondFactor(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if notice != accounts.NoticeSkipped || len(mailer.sent) != 0 || len(store.actions) == 0 {
		t.Fatalf("second factor: notice %q, mails %d, writes %v; want skipped, none, the reset done", notice, len(mailer.sent), store.actions)
	}
}

// TestNotifyDeactivated: the notice goes out in the person's effective
// language, resolved like /auth/me resolves it: their own choice, else the
// instance default, else English.
func TestNotifyDeactivated(t *testing.T) {
	de, en := "de", "en"
	for _, tc := range []struct {
		name      string
		recipient accounts.Recipient
		wantWord  string
	}{
		{"own choice", accounts.Recipient{Email: "a@example.test", Locale: &de, DefaultLocale: "en"}, "deaktiviert"},
		{"instance default", accounts.Recipient{Email: "a@example.test", DefaultLocale: "de"}, "deaktiviert"},
		{"own choice over the default", accounts.Recipient{Email: "a@example.test", Locale: &en, DefaultLocale: "de"}, "deactivated"},
		{"nothing set", accounts.Recipient{Email: "a@example.test"}, "deactivated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mailer := &fakeMailer{}
			notice := actionService(&fakeStore{recipient: tc.recipient}, mailer).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true)
			if notice != accounts.NoticeSent || len(mailer.sent) != 1 {
				t.Fatalf("notice %q, mails %+v", notice, mailer.sent)
			}
			if m := mailer.sent[0]; m.to != tc.recipient.Email || !strings.Contains(m.subject+m.body, tc.wantWord) {
				t.Fatalf("mail %+v, want it to %s with %q", m, tc.recipient.Email, tc.wantWord)
			}
		})
	}

	// An account that was already deactivated, or never claimed its
	// invitation, is told nothing.
	skipped := &fakeMailer{}
	if notice := actionService(&fakeStore{recipient: accounts.Recipient{Email: "a@example.test"}}, skipped).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), false); notice != accounts.NoticeSkipped || len(skipped.sent) != 0 {
		t.Fatalf("not active before: notice %q, mails %d; want skipped and none", notice, len(skipped.sent))
	}

	if notice := actionService(&fakeStore{}, &fakeMailer{unconfigured: true}).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true); notice != accounts.NoticeNotConfigured {
		t.Fatalf("without mail: notice %q, want not_configured", notice)
	}
	// The settings went away between the check and the send.
	gone := &fakeMailer{sendErr: fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured)}
	if notice := actionService(&fakeStore{}, gone).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true); notice != accounts.NoticeNotConfigured {
		t.Fatalf("mail settings removed before the send: notice %q, want not_configured", notice)
	}
}
