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
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	Deliveries(ctx context.Context, orgID, id uuid.UUID, limit int) ([]Delivery, error)
	Resolve(ctx context.Context, id uuid.UUID) (Trigger, []byte, error)
	TakeToken(ctx context.Context, orgID, id uuid.UUID, rl RateLimit, now time.Time) (bool, time.Duration, error)
	RecordDelivery(ctx context.Context, d Delivery, throttle bool) error
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
	// IPLimit bounds deliveries per source address across every trigger
	// (spec §9 "per-IP rate limits"); nil disables it (unit tests).
	IPLimit *clientip.Limiter
	Now     func() time.Time
	Rand    io.Reader
	Log     *slog.Logger
}

type Service struct{ d Deps }

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
	t, err := s.build(ctx, orgID, s.newID(), in)
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
	cur, err := s.d.Store.Get(ctx, orgID, id)
	if err != nil {
		return Trigger{}, err
	}
	if in.Kind != cur.Kind || !sameID(in.TemplateID, cur.TemplateID) || !sameID(in.RepoID, cur.RepoID) {
		return Trigger{}, fmt.Errorf("%w: a trigger's kind and target cannot change; create a new trigger", ErrInvalidInput)
	}
	t, err := s.build(ctx, orgID, id, in)
	if err != nil {
		return Trigger{}, err
	}
	t.CreatedBy, t.CreatedAt = cur.CreatedBy, cur.CreatedAt
	return s.d.Store.Update(ctx, t, revision)
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
	if _, ok := presets[in.Preset]; !ok {
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

func sameID(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
