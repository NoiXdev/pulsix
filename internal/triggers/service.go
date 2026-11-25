package triggers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
)

type Store interface {
	Create(ctx context.Context, t Trigger, sealedSecret []byte) (Trigger, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Trigger, error)
	List(ctx context.Context, orgID uuid.UUID, f ListFilter) ([]Trigger, error)
	Update(ctx context.Context, t Trigger, revision int64) (Trigger, error)
	SetSecret(ctx context.Context, orgID, id uuid.UUID, sealed []byte) (Trigger, error)
	// SetSecretAt is SetSecret at revision (ErrRevisionMismatch when the
	// trigger changed since).
	SetSecretAt(ctx context.Context, orgID, id uuid.UUID, revision int64, sealed []byte) (Trigger, error)
	// Disable turns the trigger off; a disabled one is returned as it is.
	Disable(ctx context.Context, orgID, id uuid.UUID) (Trigger, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	Deliveries(ctx context.Context, orgID, id uuid.UUID, limit int) ([]Delivery, error)
	Resolve(ctx context.Context, id uuid.UUID) (Trigger, []byte, error)
	TakeToken(ctx context.Context, orgID, id uuid.UUID, rl RateLimit, now time.Time) (bool, time.Duration, error)
	// RecordDelivery appends a delivery row. Which outcomes are
	// throttled to one row per trigger and minute is decided ONCE, by
	// trigger_deliveries_throttle_idx's own outcome list — it used to
	// be restated as a bool at every call site, which the store then
	// ignored, so the two could disagree with nothing to notice
	// (backlog 2.68).
	RecordDelivery(ctx context.Context, d Delivery) error
	// NoteRateLimited amends an already-recorded delivery row to say the
	// per-address budget was spent after that row's own outcome was
	// decided (Receive's own comment on why). It matches on (org, trigger,
	// outcome, minute) — the same key trigger_deliveries_throttle_idx
	// dedups on — rather than a specific delivery id, since the id the
	// caller minted for its own attempt is usually the very one that got
	// deduplicated into an earlier call's row. A call with nothing to
	// amend (no such row — outcome, say, OutcomeNotFound never has one) is
	// a harmless no-op, not an error.
	NoteRateLimited(ctx context.Context, orgID, triggerID uuid.UUID, outcome Outcome, at time.Time) error
	// InTx runs fn in one write transaction every store on the same
	// database joins.
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
	// DisableForReview disables a trigger with DisabledForReview (an
	// enabled one, or one a person disabled); ok is false (the trigger as
	// it is) when it was disabled for review already.
	DisableForReview(ctx context.Context, orgID, id uuid.UUID) (Trigger, bool, error)
	// ClearReasonAt clears the reason at revision and leaves the trigger
	// disabled (ErrRevisionMismatch).
	ClearReasonAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error)
	// EnableAt enables the trigger at revision and clears its reason.
	EnableAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error)
	// ListForReview lists the triggers that run any of templateIDs or
	// sync repoID.
	ListForReview(ctx context.Context, orgID uuid.UUID, templateIDs []uuid.UUID, repoID *uuid.UUID) ([]Trigger, error)
}

// Reviews is told when a person enables a trigger an assistant's change
// disabled for review (spec 2026-11-25 C2): its open review is closed as
// decided by that person, in the update's transaction.
type Reviews interface {
	TriggerEnabled(ctx context.Context, orgID, triggerID, by uuid.UUID) error
}

type TemplateChecker interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (templates.Template, error)
	ValidateOverrides(ctx context.Context, orgID, templateID uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error)
}

type RepoLookup interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (repos.Repo, error)
}

type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

type Runner interface {
	EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error)
	EnqueueSyncWith(ctx context.Context, orgID, repoID uuid.UUID, req jobs.RunRequest) (jobs.Job, error)
	HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error)
	HasActiveSync(ctx context.Context, orgID, repoID uuid.UUID) (bool, error)
}

type Auditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// Deps wires the service; Now, Rand and Log default when nil.
type Deps struct {
	Store     Store
	Templates TemplateChecker
	Repos     RepoLookup
	Box       Sealer
	Runs      Runner
	Audit     Auditor
	PublicURL string
	// IPLimit bounds failed calls per source address across every
	// trigger (spec §9 "per-IP rate limits"; see Receive); nil disables it
	// (unit tests).
	IPLimit *clientip.Limiter
	Now     func() time.Time
	Rand    io.Reader
	Log     *slog.Logger
}

type Service struct {
	d Deps
	// reviews closes the review of a trigger a person enables
	// (SetReviews); nil closes nothing.
	reviews Reviews
}

// SetReviews wires what a person's enabling of a trigger disabled for
// review closes.
func (s *Service) SetReviews(r Reviews) { s.reviews = r }

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Rand == nil {
		d.Rand = rand.Reader
	}
	d.PublicURL = strings.TrimRight(d.PublicURL, "/")
	return &Service{d: d}
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.d.Rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// newSecret draws 32 random bytes, base64url without padding (43 chars).
func (s *Service) newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.d.Rand, b); err != nil {
		return "", fmt.Errorf("draw trigger secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// URL is where a sender posts to.
func (s *Service) URL(t Trigger) string {
	if t.Kind == KindRepoSync {
		return s.d.PublicURL + "/api/v1/hooks/repos/" + t.ID.String()
	}
	return s.d.PublicURL + "/api/v1/hooks/jobs/" + t.ID.String()
}

// Create validates and stores a trigger; the secret is returned once.
func (s *Service) Create(ctx context.Context, orgID, actor uuid.UUID, in Input) (Created, error) {
	return s.CreateAs(ctx, orgID, s.newID(), actor, in)
}

// CreateAs is Create under id: an approved trigger request creates the
// trigger whose id it took when it was made, so the picture the person saw
// and the trigger name the same URL.
func (s *Service) CreateAs(ctx context.Context, orgID, id, actor uuid.UUID, in Input) (Created, error) {
	t, err := s.build(ctx, orgID, id, in)
	if err != nil {
		return Created{}, err
	}
	t.CreatedBy = &actor
	secret, sealed, err := s.sealNewSecret(orgID, t.ID)
	if err != nil {
		return Created{}, err
	}
	stored, err := s.d.Store.Create(ctx, t, sealed)
	if err != nil {
		return Created{}, err
	}
	return Created{Trigger: stored, Secret: secret, URL: s.URL(stored)}, nil
}

// Update replaces the editable fields; kind and target are immutable.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Trigger, error) {
	return s.UpdateBy(ctx, orgID, id, uuid.Nil, revision, in)
}

// UpdateBy is Update by the person by. A person may enable a trigger an
// assistant's change disabled for review (DisabledForReview) directly:
// the update clears the reason and closes the review as decided by them,
// in one transaction.
func (s *Service) UpdateBy(ctx context.Context, orgID, id, by uuid.UUID, revision int64, in Input) (Trigger, error) {
	cur, err := s.d.Store.Get(ctx, orgID, id)
	if err != nil {
		return Trigger{}, err
	}
	if in.Kind != cur.Kind || !sameID(in.TemplateID, cur.TemplateID) || !sameID(in.RepoID, cur.RepoID) {
		return Trigger{}, errTargetFixed
	}
	t, err := s.build(ctx, orgID, id, in)
	if err != nil {
		return Trigger{}, err
	}
	t.CreatedBy, t.CreatedAt = cur.CreatedBy, cur.CreatedAt
	if cur.DisabledReason == "" || !t.Enabled || s.reviews == nil {
		return s.d.Store.Update(ctx, t, revision)
	}
	var out Trigger
	err = s.d.Store.InTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.d.Store.Update(ctx, t, revision); err != nil {
			return err
		}
		if err := s.reviews.TriggerEnabled(ctx, orgID, id, by); err != nil {
			return fmt.Errorf("close the trigger's review: %w", err)
		}
		return nil
	})
	return out, err
}

// DisableForReview disables a trigger because an assistant changed what it
// runs or syncs (spec 2026-11-25 C2). A trigger a person disabled gets the
// reason as well, so the notice shows before anyone enables it. ok is
// false, and the trigger comes back as it is, when it was disabled for
// review already.
func (s *Service) DisableForReview(ctx context.Context, orgID, id uuid.UUID) (Trigger, bool, error) {
	return s.d.Store.DisableForReview(ctx, orgID, id)
}

// AcceptReviewAt is an approved review of a trigger a person had disabled
// before the assistant's change: the reason goes, the trigger stays
// disabled, at the revision the approver saw.
func (s *Service) AcceptReviewAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error) {
	return s.d.Store.ClearReasonAt(ctx, orgID, id, revision)
}

// EnableAt enables the trigger at the revision a person saw (an approved
// review) and clears why it was disabled.
func (s *Service) EnableAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error) {
	return s.d.Store.EnableAt(ctx, orgID, id, revision)
}

// ListForReview lists the triggers that run any of templateIDs or sync
// repoID (nil for none).
func (s *Service) ListForReview(ctx context.Context, orgID uuid.UUID, templateIDs []uuid.UUID, repoID *uuid.UUID) ([]Trigger, error) {
	return s.d.Store.ListForReview(ctx, orgID, templateIDs, repoID)
}

// RotateSecret replaces the secret; the new one is returned once.
func (s *Service) RotateSecret(ctx context.Context, orgID, id uuid.UUID) (Created, error) {
	secret, sealed, err := s.sealNewSecret(orgID, id)
	if err != nil {
		return Created{}, err
	}
	t, err := s.d.Store.SetSecret(ctx, orgID, id, sealed)
	if err != nil {
		return Created{}, err
	}
	return Created{Trigger: t, Secret: secret, URL: s.URL(t)}, nil
}

// RotateSecretAt is RotateSecret of the trigger at revision: an approved
// rotation request replaces the secret of exactly the trigger the person
// saw (ErrRevisionMismatch when it changed since).
func (s *Service) RotateSecretAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Created, error) {
	secret, sealed, err := s.sealNewSecret(orgID, id)
	if err != nil {
		return Created{}, err
	}
	t, err := s.d.Store.SetSecretAt(ctx, orgID, id, revision, sealed)
	if err != nil {
		return Created{}, err
	}
	return Created{Trigger: t, Secret: secret, URL: s.URL(t)}, nil
}

// Disable turns a trigger off at once, whatever its revision: it only
// stops runs (spec 2026-11-25 assistant API gaps, D1).
func (s *Service) Disable(ctx context.Context, orgID, id uuid.UUID) (Trigger, error) {
	return s.d.Store.Disable(ctx, orgID, id)
}

// Validate checks in as Create (a new id) or Update (an existing
// trigger's id) would and writes nothing: it returns the trigger as it
// would be stored, defaults filled in. A name another trigger of the
// organization has is ErrNameTaken.
func (s *Service) Validate(ctx context.Context, orgID, id uuid.UUID, in Input) (Trigger, error) {
	cur, err := s.d.Store.Get(ctx, orgID, id)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Trigger{}, err
	case in.Kind != cur.Kind || !sameID(in.TemplateID, cur.TemplateID) || !sameID(in.RepoID, cur.RepoID):
		return Trigger{}, errTargetFixed
	}
	t, err := s.build(ctx, orgID, id, in)
	if err != nil {
		return Trigger{}, err
	}
	all, err := s.d.Store.List(ctx, orgID, ListFilter{})
	if err != nil {
		return Trigger{}, err
	}
	for _, o := range all {
		if o.ID != id && o.Name == t.Name {
			return Trigger{}, ErrNameTaken
		}
	}
	return t, nil
}

func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Trigger, error) {
	return s.d.Store.Get(ctx, orgID, id)
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID, f ListFilter) ([]Trigger, error) {
	return s.d.Store.List(ctx, orgID, f)
}

func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.d.Store.Delete(ctx, orgID, id)
}

// maxDeliveriesLimit bounds Deliveries so a negative or absurdly large
// caller-supplied value cannot reach the store as-is (Task 9 review, Minor
// 6): a negative value is an SQL error there, and a value above MaxInt32
// wraps when cast.
const maxDeliveriesLimit = 500

func (s *Service) Deliveries(ctx context.Context, orgID, id uuid.UUID, limit int) ([]Delivery, error) {
	if _, err := s.d.Store.Get(ctx, orgID, id); err != nil {
		return nil, err
	}
	return s.d.Store.Deliveries(ctx, orgID, id, clampDeliveriesLimit(limit))
}

func clampDeliveriesLimit(limit int) int {
	switch {
	case limit < 1:
		return 1
	case limit > maxDeliveriesLimit:
		return maxDeliveriesLimit
	default:
		return limit
	}
}

func (s *Service) sealNewSecret(orgID, id uuid.UUID) (string, []byte, error) {
	secret, err := s.newSecret()
	if err != nil {
		return "", nil, err
	}
	sealed, err := s.d.Box.SealAAD([]byte(secret), AAD(orgID, id))
	if err != nil {
		return "", nil, fmt.Errorf("seal trigger secret: %w", err)
	}
	return secret, sealed, nil
}

func (s *Service) build(ctx context.Context, orgID, id uuid.UUID, in Input) (Trigger, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return Trigger{}, err
	}
	if _, ok := preset(in.Preset); !ok {
		return Trigger{}, fmt.Errorf("%w: preset must be pulsix, github, gitea, gitlab or bearer", ErrInvalidInput)
	}
	if in.TimestampHeader != "" {
		if !headerNameRE.MatchString(in.TimestampHeader) {
			return Trigger{}, fmt.Errorf("%w: timestamp header must be a header name", ErrInvalidInput)
		}
		// Only the pulsix preset can use a timestamp header: GitHub and
		// Gitea never sign "<timestamp>.<body>" (their real senders would
		// fail every genuine delivery), and GitLab's and bearer's token
		// does not bind a timestamp at all, so the replay window would add
		// nothing (Task 9 review, Minor 3).
		if in.Preset != PresetPulsix {
			return Trigger{}, fmt.Errorf("%w: a timestamp header is only supported for the pulsix preset", ErrInvalidInput)
		}
	}
	cidrs, err := parseCIDRs(in.AllowCIDRs)
	if err != nil {
		return Trigger{}, err
	}
	rl, err := normalizeRateLimit(in.RateLimit)
	if err != nil {
		return Trigger{}, err
	}
	onActive := in.OnActive
	switch onActive {
	case "":
		onActive = OnActiveQueue
	case OnActiveQueue, OnActiveSkip:
	default:
		return Trigger{}, fmt.Errorf("%w: on_active must be queue or skip", ErrInvalidInput)
	}
	if _, err := compileMapping(in.Mapping); err != nil {
		return Trigger{}, err
	}
	switch in.Kind {
	case KindTemplateRun:
		if in.TemplateID == nil || in.RepoID != nil {
			return Trigger{}, fmt.Errorf("%w: a template_run trigger names a template and no repository", ErrInvalidInput)
		}
		tmpl, err := s.d.Templates.Get(ctx, orgID, *in.TemplateID)
		if errors.Is(err, templates.ErrNotFound) {
			return Trigger{}, fmt.Errorf("%w: the job template does not exist", ErrInvalidInput)
		}
		if err != nil {
			return Trigger{}, err
		}
		if err := checkMappingTargets(in.Mapping, tmpl); err != nil {
			return Trigger{}, err
		}
	case KindRepoSync:
		if in.RepoID == nil || in.TemplateID != nil {
			return Trigger{}, fmt.Errorf("%w: a repo_sync trigger names a repository and no template", ErrInvalidInput)
		}
		if !in.Mapping.empty() {
			return Trigger{}, fmt.Errorf("%w: repo_sync triggers take no mapping", ErrInvalidInput)
		}
		if _, err := s.d.Repos.Get(ctx, orgID, *in.RepoID); errors.Is(err, repos.ErrNotFound) {
			return Trigger{}, fmt.Errorf("%w: the repository does not exist", ErrInvalidInput)
		} else if err != nil {
			return Trigger{}, err
		}
	default:
		return Trigger{}, fmt.Errorf("%w: kind must be template_run or repo_sync", ErrInvalidInput)
	}
	return Trigger{
		ID: id, OrgID: orgID, Kind: in.Kind, TemplateID: in.TemplateID, RepoID: in.RepoID, Name: name,
		Enabled: in.Enabled, Preset: in.Preset, TimestampHeader: in.TimestampHeader, AllowCIDRs: cidrs,
		Mapping: in.Mapping, OnActive: onActive, RateLimit: rl,
	}, nil
}

// errTargetFixed refuses a change of a trigger's kind or target.
var errTargetFixed = fmt.Errorf("%w: a trigger's kind and target cannot change; create a new trigger", ErrInvalidInput)

func sameID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
