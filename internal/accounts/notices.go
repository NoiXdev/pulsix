package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/locale"
	"github.com/noixdev/pulsix/internal/mail"
)

// Notice reports the best-effort mail that tells a person what an
// administrator did to their account.
type Notice string

// The notice outcomes. The action stands in every case.
const (
	NoticeSent          Notice = "sent"
	NoticeNotConfigured Notice = "not_configured"
	NoticeFailed        Notice = "failed"
)

// Recipient is where an account notice goes: the address, the account's
// own language (nil when it never chose one) and the instance default (""
// when unset).
type Recipient struct {
	Email         string
	Locale        *string
	DefaultLocale string
}

// noticeKind names the administrator's action a notice reports.
type noticeKind string

const (
	noticeTemporaryPassword noticeKind = "temporary_password"
	noticeSecondFactorReset noticeKind = "second_factor_reset"
	noticeDeactivated       noticeKind = "deactivated"
)

// noticeSendTimeout bounds one notice's SMTP conversation. The send is
// detached from the request, like the invitation mail's: the action has
// happened, and whether the person hears of it must not depend on the
// administrator's browser staying on the page.
const noticeSendTimeout = 45 * time.Second

// noticeTexts are the subject and body of each notice, by language. They
// say what happened and never carry a secret: a temporary password reaches
// the person only through the administrator.
var noticeTexts = map[string]map[noticeKind][2]string{
	locale.EN: {
		noticeTemporaryPassword: {
			"Your Pulsix password was reset",
			"An administrator set a temporary password for your Pulsix account and signed you out everywhere.\n\n" +
				"Ask your administrator for the temporary password. It works for 24 hours, and you choose a new password when you sign in.\n\n" +
				"Sign in: %s\n\nIf you did not expect this, contact your administrator.",
		},
		noticeSecondFactorReset: {
			"Your Pulsix second factor was reset",
			"An administrator removed the second factors of your Pulsix account (authenticator app, recovery codes and passkeys) and signed you out everywhere.\n\n" +
				"At your next sign-in you set up a new second factor.\n\n" +
				"Sign in: %s\n\nIf you did not expect this, contact your administrator at once.",
		},
		noticeDeactivated: {
			"Your Pulsix account was deactivated",
			"An administrator deactivated your account on %s. You can no longer sign in there.\n\n" +
				"If you think this is a mistake, contact your administrator.",
		},
	},
	locale.DE: {
		noticeTemporaryPassword: {
			"Ihr Pulsix-Passwort wurde zurückgesetzt",
			"Ihre Administration hat für Ihr Pulsix-Konto ein temporäres Passwort gesetzt und Sie überall abgemeldet.\n\n" +
				"Fragen Sie Ihre Administration nach dem temporären Passwort. Es gilt 24 Stunden, und bei der Anmeldung wählen Sie ein neues Passwort.\n\n" +
				"Anmelden: %s\n\nWenn Sie damit nicht gerechnet haben, wenden Sie sich an Ihre Administration.",
		},
		noticeSecondFactorReset: {
			"Ihr zweiter Faktor für Pulsix wurde zurückgesetzt",
			"Ihre Administration hat die zweiten Faktoren Ihres Pulsix-Kontos (Authenticator-App, Wiederherstellungscodes und Passkeys) entfernt und Sie überall abgemeldet.\n\n" +
				"Bei der nächsten Anmeldung richten Sie einen neuen zweiten Faktor ein.\n\n" +
				"Anmelden: %s\n\nWenn Sie damit nicht gerechnet haben, wenden Sie sich sofort an Ihre Administration.",
		},
		noticeDeactivated: {
			"Ihr Pulsix-Konto wurde deaktiviert",
			"Ihre Administration hat Ihr Konto auf %s deaktiviert. Sie können sich dort nicht mehr anmelden.\n\n" +
				"Wenn Sie das für einen Irrtum halten, wenden Sie sich an Ihre Administration.",
		},
	},
}

// NotifyDeactivated tells the person that their account was deactivated.
// The deactivate route calls it once tenancy.Service.DeactivateUser
// succeeded; deactivation itself stays in tenancy.
func (s *Service) NotifyDeactivated(ctx context.Context, target uuid.UUID) Notice {
	return s.notify(ctx, target, noticeDeactivated)
}

// notify mails the person the notice of kind in their effective language,
// resolved as /auth/me resolves it. It is best-effort: a failure is logged,
// without the mail's body, and reported as NoticeFailed, never as an error,
// since the action it reports has already happened.
func (s *Service) notify(ctx context.Context, target uuid.UUID, kind noticeKind) Notice {
	log := func(err error) Notice {
		slog.WarnContext(ctx, "account notice not sent", "notice", string(kind), "user_id", target.String(), "error", err)
		return NoticeFailed
	}
	configured, err := s.mailer.Configured(ctx)
	if err != nil {
		return log(fmt.Errorf("check outgoing mail: %w", err))
	}
	if !configured {
		return NoticeNotConfigured
	}
	to, err := s.store.NoticeRecipient(ctx, target)
	if err != nil {
		return log(fmt.Errorf("read recipient: %w", err))
	}
	text := noticeTexts[locale.Resolve(to.Locale, to.DefaultLocale)][kind]
	body := fmt.Sprintf(text[1], s.publicURL)

	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noticeSendTimeout)
	defer cancel()
	if err := s.mailer.Send(sendCtx, to.Email, text[0], body); err != nil {
		if errors.Is(err, mail.ErrNotConfigured) {
			// The settings went away between the check and the send.
			return NoticeNotConfigured
		}
		return log(err)
	}
	return NoticeSent
}
