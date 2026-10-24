// Package triggers implements incoming webhook triggers (spec 2026-09-18
// §3): a secret-guarded URL per job template (template_run) or git
// repository (repo_sync) that starts a run or a sync, with an IP
// allowlist, HMAC or token authentication, a replay window, a shared rate
// limit and a JSONPath mapping from the payload onto extra vars and limit.
package triggers

import (
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrInvalidInput     = errors.New("invalid input")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
)

type Kind string

const (
	KindTemplateRun Kind = "template_run"
	KindRepoSync    Kind = "repo_sync"
)

type Auth string

const (
	AuthHMAC  Auth = "hmac"
	AuthToken Auth = "token"
)

type Preset string

const (
	PresetPulsix Preset = "pulsix"
	PresetGitHub Preset = "github"
	PresetGitea  Preset = "gitea"
	PresetGitLab Preset = "gitlab"
	PresetBearer Preset = "bearer"
)

// presetSpec is how a preset authenticates: the header the credential
// travels in and the prefix its value carries.
type presetSpec struct {
	auth   Auth
	header string
	prefix string
}

// preset resolves a preset's signature arrangement. A switch rather
// than a package-level map: a map var is writable by anything in the
// package, and what it decides is how a webhook proves itself —
// CLAUDE.md's "no package-level state" (backlog 2.68). A switch also
// allocates nothing, which a function returning a fresh map would do
// on every Auth() and SignatureHeader() call.
func preset(p Preset) (presetSpec, bool) {
	switch p {
	case PresetPulsix:
		return presetSpec{auth: AuthHMAC, header: "X-Pulsix-Signature-256", prefix: "sha256="}, true
	case PresetGitHub:
		return presetSpec{auth: AuthHMAC, header: "X-Hub-Signature-256", prefix: "sha256="}, true
	case PresetGitea:
		return presetSpec{auth: AuthHMAC, header: "X-Gitea-Signature"}, true
	case PresetGitLab:
		return presetSpec{auth: AuthToken, header: "X-Gitlab-Token"}, true
	case PresetBearer:
		return presetSpec{auth: AuthToken, header: "Authorization", prefix: "Bearer "}, true
	}
	return presetSpec{}, false
}

type OnActive string

const (
	OnActiveQueue OnActive = "queue"
	OnActiveSkip  OnActive = "skip"
)

type Outcome string

const (
	OutcomeTriggered      Outcome = "triggered"
	OutcomeSkippedActive  Outcome = "skipped_active"
	OutcomeRejectedIP     Outcome = "rejected_ip"
	OutcomeRejectedAuth   Outcome = "rejected_auth"
	OutcomeRateLimited    Outcome = "rate_limited"
	OutcomeInvalidPayload Outcome = "invalid_payload"
	OutcomeDisabled       Outcome = "disabled"
	OutcomeFailed         Outcome = "failed"
	// OutcomeNotFound answers an unknown id or a kind/endpoint mismatch;
	// it is never stored (there is no trigger to attach it to).
	OutcomeNotFound Outcome = "not_found"
)

// hidden reports whether the outcome answers the uniform empty 404: the
// refusals that must not reveal whether a trigger exists.
func (o Outcome) hidden() bool {
	switch o {
	case OutcomeNotFound, OutcomeDisabled, OutcomeRejectedIP, OutcomeRejectedAuth:
		return true
	}
	return false
}

type RateLimit struct {
	Count int    `json:"count"`
	Per   string `json:"per"`
}

// Mapping maps payload JSONPaths onto top-level extra vars and the limit.
type Mapping struct {
	ExtraVars map[string]string `json:"extra_vars,omitempty"`
	Limit     string            `json:"limit,omitempty"`
}

func (m Mapping) empty() bool { return len(m.ExtraVars) == 0 && m.Limit == "" }

// Trigger is a stored trigger without its secret.
type Trigger struct {
	ID, OrgID            uuid.UUID
	Kind                 Kind
	TemplateID, RepoID   *uuid.UUID
	Name                 string
	Enabled              bool
	Preset               Preset
	TimestampHeader      string
	AllowCIDRs           []netip.Prefix
	Mapping              Mapping
	OnActive             OnActive
	RateLimit            RateLimit
	CreatedBy            *uuid.UUID
	CreatedAt, UpdatedAt time.Time
	Revision             int64
}

// Auth is the preset's authentication mode.
func (t Trigger) Auth() Auth { spec, _ := preset(t.Preset); return spec.auth }

// SignatureHeader is the header the credential travels in.
func (t Trigger) SignatureHeader() string { spec, _ := preset(t.Preset); return spec.header }

// Input is what an operator submits.
type Input struct {
	Kind            Kind
	TemplateID      *uuid.UUID
	RepoID          *uuid.UUID
	Name            string
	Enabled         bool
	Preset          Preset
	TimestampHeader string
	AllowCIDRs      []string
	Mapping         Mapping
	OnActive        OnActive
	RateLimit       RateLimit
}

// Delivery is one received call; the body is never kept.
type Delivery struct {
	ID, OrgID, TriggerID uuid.UUID
	At                   time.Time
	SourceIP             string
	Outcome              Outcome
	Reason               string
	JobGroupID           *uuid.UUID
}

// Created carries the secret, shown once, and the URL.
type Created struct {
	Trigger Trigger
	Secret  string
	URL     string
}

type ListFilter struct {
	TemplateID *uuid.UUID
	RepoID     *uuid.UUID
}

// AAD is the associated data a trigger's secret is sealed with.
func AAD(orgID, id uuid.UUID) []byte {
	return []byte("trigger:" + orgID.String() + ":" + id.String())
}
