// Package setup implements the first-run wizard: it creates the instance
// administrator and the first organization, and stores instance-wide
// settings (SMTP today, branding later) sealed with the instance master
// key. It is the one privileged operation the API serves with no principal
// at all, gated only on "no instance administrator exists yet".
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// Version is reported by Status so the SPA (and support requests) can tell
// which build completed the wizard. It intentionally duplicates the
// literal huma API version in internal/api/api.go rather than importing
// it: package api depends on this package (Deps.Setup), and this package
// must not depend back on it.
const Version = "0.1.0"

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	// ErrAlreadyCompleted means an instance administrator already exists —
	// Complete refuses to run a second time, whether the two calls arrive
	// one after another or genuinely concurrently (see SQLStore.InTx).
	ErrAlreadyCompleted = errors.New("setup already completed")
	// ErrInvalidInput is wrapped with a human-readable reason by
	// Input.validate; callers should present err.Error() directly rather
	// than a generic "bad request".
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound is returned by LoadSMTP (via Store.LoadSetting) when no
	// value has ever been saved for the requested key.
	ErrNotFound = errors.New("not found")
)

// minPasswordLength is the wizard's password floor. It exists nowhere else
// in this codebase (see auth_handlers.go's loginInput.Password comment):
// this is the one place a brand-new credential is actually created, so it
// is the one place a strength floor belongs.
const minPasswordLength = 12

// maxSlugLength bounds Input.OrgSlug the same way orgCreateInput does in
// package api (63 there), but tighter: this pattern's own capture group is
// bounded to 40 total characters (1 plus up to 38 plus 1), matching a DNS
// label-style rule rather than the runtime org-creation route's looser one.
const maxSlugLength = 40

// slugPattern is the wizard's slug rule: lowercase letters and digits,
// optionally with single hyphens in the middle, 1-40 characters, never
// starting or ending with a hyphen.
var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])?$`)

// Input is what the wizard needs to complete first-run setup.
type Input struct {
	AdminEmail    string
	AdminPassword string
	AdminName     string
	OrgSlug       string
	OrgName       string
	// SMTP is optional: an operator can skip mail configuration at setup
	// time and add it later through PUT /admin/settings/smtp.
	SMTP *mail.SMTPSettings
}

// validate reports every problem with in at once (not just the first), so
// a caller gets a single useful message instead of fixing one field per
// round trip.
func (in Input) validate() error {
	var problems []string
	if !strings.Contains(in.AdminEmail, "@") {
		problems = append(problems, "admin email is required")
	}
	if strings.TrimSpace(in.AdminName) == "" {
		problems = append(problems, "admin name is required")
	}
	if len(in.AdminPassword) < minPasswordLength {
		problems = append(problems, fmt.Sprintf("admin password must be at least %d characters", minPasswordLength))
	}
	if len(in.OrgSlug) > maxSlugLength || !slugPattern.MatchString(in.OrgSlug) {
		problems = append(problems, "organization slug must be lowercase letters, digits and hyphens, 1-40 characters, and must not start or end with a hyphen")
	}
	if strings.TrimSpace(in.OrgName) == "" {
		problems = append(problems, "organization name is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidInput, strings.Join(problems, "; "))
	}
	return nil
}

// Result is what Complete hands back to the transport layer.
type Result struct {
	UserID uuid.UUID
	OrgID  uuid.UUID
	// Cookie is a ready-to-set session cookie value, opened with
	// mfaComplete = false: the newly created administrator has no second
	// factor enrolled yet, so the SPA must send them straight into TOTP or
	// passkey enrolment before anything else works (every other route is
	// gated on requireMFAComplete).
	Cookie string
}

// Status is what GET /setup/status reports.
type Status struct {
	// Completed is true once an instance administrator exists.
	Completed bool
	// AdminMFAEnrolled is only meaningful once Completed is true: it lets
	// the SPA keep insisting on second-factor enrolment across reloads,
	// the same way a pending session does mid-wizard.
	AdminMFAEnrolled bool
	Version          string
}

// AdminUser is what Store.CreateAdmin persists: the wizard's one and only
// instance administrator.
type AdminUser struct {
	ID           uuid.UUID
	Email        string
	DisplayName  string
	PasswordHash string
}

// Store is the persistence contract Service needs. InTx is the atomicity
// boundary Complete runs inside: every write it makes — the admin user, the
// first org, the owner membership, and any SMTP settings — commits together
// or not at all, and the real SQL store additionally serializes concurrent
// callers against each other (see SQLStore.InTx's own doc comment), so two
// requests racing to complete setup can never both succeed.
type Store interface {
	InTx(ctx context.Context, fn func(Store) error) error
	// AdminExists reports whether an instance administrator has already
	// been created — the gate Complete checks first, inside InTx, and
	// what Status reports as Completed.
	AdminExists(ctx context.Context) (bool, error)
	AdminMFAEnrolled(ctx context.Context) (bool, error)
	CreateAdmin(ctx context.Context, u AdminUser) error
	CreateOrg(ctx context.Context, o tenancy.Org) error
	AddMembership(ctx context.Context, orgID, userID uuid.UUID, role tenancy.Role) error
	// SaveSetting and LoadSetting persist and retrieve one JSON blob by
	// key ("smtp" today). LoadSetting returns ErrNotFound when key has
	// never been saved — never a swallowed error, the same contract every
	// other store in this codebase gives a missing row.
	SaveSetting(ctx context.Context, key string, value []byte) error
	LoadSetting(ctx context.Context, key string) ([]byte, error)
}

// SessionStore is the subset of *auth.Sessions Service needs — just enough
// to open the admin's first session, mirroring auth.Service's own
// SessionStore seam.
type SessionStore interface {
	Create(ctx context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error)
}

// smtpSettingsKey is the instance_settings row SMTP configuration lives
// under.
const smtpSettingsKey = "smtp"

// Service implements the first-run wizard and instance settings.
type Service struct {
	store    Store
	sessions SessionStore
	box      *crypto.Box
	now      func() time.Time
}

// NewService wires the store, session opener, the box used to seal secrets
// inside instance settings (the SMTP password), and the clock.
func NewService(store Store, sessions SessionStore, box *crypto.Box, now func() time.Time) *Service {
	return &Service{store: store, sessions: sessions, box: box, now: now}
}

// Status reports whether the wizard has run and, if so, whether the
// instance administrator has enrolled a second factor yet.
func (s *Service) Status(ctx context.Context) (Status, error) {
	completed, err := s.store.AdminExists(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("check admin exists: %w", err)
	}
	var enrolled bool
	if completed {
		enrolled, err = s.store.AdminMFAEnrolled(ctx)
		if err != nil {
			return Status{}, fmt.Errorf("check admin mfa enrolment: %w", err)
		}
	}
	return Status{Completed: completed, AdminMFAEnrolled: enrolled, Version: Version}, nil
}

// Complete runs the wizard: it validates in, then atomically (inside one
// Store.InTx transaction) creates the instance administrator, the first
// organization with the administrator as its owner, and — if in.SMTP is
// set — the instance's SMTP settings, sealed with the master key. It
// refuses with ErrAlreadyCompleted if an administrator already exists,
// whether that is discovered before this call started or only once the
// transaction actually runs (the concurrent case InTx exists to close).
//
// The admin password is hashed before the transaction begins: argon2id
// hashing needs no database access, and doing it outside InTx keeps the
// transaction — and so the lock SQLStore.InTx holds for its duration —
// as short as possible.
func (s *Service) Complete(ctx context.Context, in Input) (Result, error) {
	if err := in.validate(); err != nil {
		return Result{}, err
	}
	hash, err := auth.HashPassword(in.AdminPassword)
	if err != nil {
		return Result{}, fmt.Errorf("hash admin password: %w", err)
	}

	var res Result
	err = s.store.InTx(ctx, func(tx Store) error {
		exists, err := tx.AdminExists(ctx)
		if err != nil {
			return fmt.Errorf("check admin exists: %w", err)
		}
		if exists {
			return ErrAlreadyCompleted
		}

		userID := uuid.Must(uuid.NewV7())
		admin := AdminUser{ID: userID, Email: strings.ToLower(in.AdminEmail), DisplayName: in.AdminName, PasswordHash: hash}
		if err := tx.CreateAdmin(ctx, admin); err != nil {
			return fmt.Errorf("create instance admin: %w", err)
		}

		org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: in.OrgSlug, Name: in.OrgName, CreatedAt: s.now()}
		if err := tx.CreateOrg(ctx, org); err != nil {
			return fmt.Errorf("create first organization: %w", err)
		}
		if err := tx.AddMembership(ctx, org.ID, userID, tenancy.RoleOwner); err != nil {
			return fmt.Errorf("add owner membership: %w", err)
		}

		if in.SMTP != nil {
			if err := saveSMTP(ctx, tx, s.box, *in.SMTP); err != nil {
				return err
			}
		}

		res.UserID, res.OrgID = userID, org.ID
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	// mfaComplete = false: see Result.Cookie's own doc comment.
	_, cookie, err := s.sessions.Create(ctx, res.UserID, false)
	if err != nil {
		return Result{}, fmt.Errorf("open admin session: %w", err)
	}
	res.Cookie = cookie
	return res, nil
}

// SaveSMTP seals in.Password with the instance master key and persists the
// settings, replacing whatever was stored before. It is used both directly
// (PUT /admin/settings/smtp, once the wizard has already run) and, through
// saveSMTP, by Complete itself when the wizard configures mail up front.
func (s *Service) SaveSMTP(ctx context.Context, in mail.SMTPSettings) error {
	return saveSMTP(ctx, s.store, s.box, in)
}

// LoadSMTP reads the stored settings back and unseals the password. It
// returns ErrNotFound (via Store.LoadSetting) when no settings have ever
// been saved — the state every fresh instance starts in.
func (s *Service) LoadSMTP(ctx context.Context) (mail.SMTPSettings, error) {
	data, err := s.store.LoadSetting(ctx, smtpSettingsKey)
	if err != nil {
		return mail.SMTPSettings{}, err
	}
	var blob smtpBlob
	if err := json.Unmarshal(data, &blob); err != nil {
		return mail.SMTPSettings{}, fmt.Errorf("unmarshal smtp settings: %w", err)
	}
	plain, err := s.box.Open(blob.PasswordSealed)
	if err != nil {
		return mail.SMTPSettings{}, fmt.Errorf("open smtp password: %w", err)
	}
	return mail.SMTPSettings{
		Host: blob.Host, Port: blob.Port, Username: blob.Username, Password: string(plain),
		From: blob.From, StartTLS: blob.StartTLS,
	}, nil
}

// smtpBlob is instance_settings' "smtp" value: every field in the clear
// except PasswordSealed, which crypto.Box.Seal produces and only
// LoadSMTP's crypto.Box.Open ever reverses. json.Marshal base64-encodes a
// []byte field automatically, so PasswordSealed never appears as the raw
// plaintext password even transiently in the JSON this produces.
type smtpBlob struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	PasswordSealed []byte `json:"password_sealed"`
	From           string `json:"from"`
	StartTLS       bool   `json:"start_tls"`
}

// saveSMTP is the shared implementation behind Service.SaveSMTP and
// Complete's own SMTP step, parameterized over which Store to write
// through (the plain store outside a transaction, or the tx-scoped one
// Complete's InTx callback receives).
func saveSMTP(ctx context.Context, store Store, box *crypto.Box, in mail.SMTPSettings) error {
	sealed, err := box.Seal([]byte(in.Password))
	if err != nil {
		return fmt.Errorf("seal smtp password: %w", err)
	}
	blob := smtpBlob{Host: in.Host, Port: in.Port, Username: in.Username, PasswordSealed: sealed, From: in.From, StartTLS: in.StartTLS}
	data, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("marshal smtp settings: %w", err)
	}
	if err := store.SaveSetting(ctx, smtpSettingsKey, data); err != nil {
		return fmt.Errorf("save smtp settings: %w", err)
	}
	return nil
}
