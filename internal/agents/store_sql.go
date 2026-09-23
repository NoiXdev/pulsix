package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB. host_agents,
// agent_certificates and agent_enrollments carry row-level security
// (migration 00037/00038), so gRPC-path writes bind the agent's org
// explicitly — the same posture internal/workers' SQLStore documents.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

func (s *SQLStore) CreateToken(ctx context.Context, t Token, secretHash []byte) (Token, error) {
	row, err := s.q.CreateAgentEnrollment(ctx, gen.CreateAgentEnrollmentParams{
		ID: t.ID, OrgID: t.OrgID, ZoneID: t.ZoneID, HostID: db.NullUUID(t.HostID),
		NameHint: nullText(t.NameHint), SecretHash: secretHash, MaxUses: int32(t.MaxUses), //nolint:gosec // 1..100, checked by the service
		ExpiresAt: t.ExpiresAt, CreatedBy: db.NullUUID(t.CreatedBy),
	})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// The composite foreign keys enforce zone and host ownership
			// at the source: a stale id reads as invalid input, exactly
			// like the API layer's own zone check.
			return Token{}, fmt.Errorf("%w: %s does not exist in the organization", ErrInvalidInput, fkSubject(db.ConstraintName(err)))
		}
		return Token{}, fmt.Errorf("create enrollment: %w", err)
	}
	return toToken(row), nil
}

// fkSubject names the referenced table a constraint guards ("zone",
// "host"), for the create-token error message.
func fkSubject(constraint string) string {
	switch {
	case strings.Contains(constraint, "zone"):
		return "zone"
	case strings.Contains(constraint, "host"):
		return "host"
	}
	return "target"
}

func (s *SQLStore) ListTokens(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Token, error) {
	rows, err := s.q.ListPendingAgentEnrollments(ctx, gen.ListPendingAgentEnrollmentsParams{OrgID: orgID, Now: now})
	if err != nil {
		return nil, fmt.Errorf("list enrollments: %w", err)
	}
	out := make([]Token, len(rows))
	for i, r := range rows {
		out[i] = toToken(r)
	}
	return out, nil
}

func (s *SQLStore) RevokeToken(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteAgentEnrollment(ctx, gen.DeleteAgentEnrollmentParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete enrollment: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnrollmentOrg resolves the token's organization, org-unscoped (the id is
// the token's public half). An unknown token is ErrTokenInvalid.
func (s *SQLStore) EnrollmentOrg(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	orgID, err := s.q.GetAgentEnrollmentOrg(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTokenInvalid
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find enrollment: %w", err)
	}
	return orgID, nil
}

// Claim locks the enrollment row for orgID, runs fn and, when it succeeds,
// consumes one use with the store's conditional UPDATE — one transaction. A
// concurrent second claim blocks on the row lock and then finds the guard
// unmatchable (exhausted, or expired by the database's own now()). fn
// receives the transaction-bound context so host work it does through
// other stores joins this transaction's atomicity. An unknown token, or
// one that belongs to another organization, is ErrTokenInvalid directly —
// ErrNotFound stays reserved for the claim's host-side failures.
func (s *SQLStore) Claim(ctx context.Context, orgID, id uuid.UUID, fn func(ctx context.Context, t Token, secretHash []byte) error) error {
	rowOrg, err := s.q.GetAgentEnrollmentOrg(ctx, id) // unscoped: the id is the lookup key
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("find enrollment: %w", err)
	}
	if rowOrg != orgID {
		// A token of another organization is invisible, not forbidden.
		return ErrTokenInvalid
	}
	return s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		row, err := s.q.LockAgentEnrollment(ctx, gen.LockAgentEnrollmentParams{OrgID: orgID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			// Withdrawn between GetAgentEnrollmentOrg and here: the same
			// refusal, so a revoke racing a claim never surfaces a raw
			// error.
			return ErrTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("lock enrollment: %w", err)
		}
		if err := fn(ctx, toToken(row), row.SecretHash); err != nil {
			return err
		}
		// The use-count guard: a conditional UPDATE, not the closure
		// above. 0 rows means another claim consumed the last use (or the
		// expiry lapsed, judged by the database's own clock) after the
		// lock was taken but before this statement ran; the whole
		// transaction — including any host the provisioner just created —
		// rolls back.
		n, err := s.q.ConsumeAgentEnrollmentUse(ctx, gen.ConsumeAgentEnrollmentUseParams{OrgID: orgID, ID: id})
		if err != nil {
			return fmt.Errorf("consume enrollment use: %w", err)
		}
		if n == 0 {
			return ErrTokenInvalid
		}
		return nil
	})
}

// Register replaces the host's active agent in one transaction: the
// previous serial is revoked, the row upserted, the new certificate row
// inserted.
func (s *SQLStore) Register(ctx context.Context, a Agent) error {
	return s.d.WithWriteTx(ctx, a.OrgID, func(ctx context.Context) error {
		if _, err := s.q.UpsertHostAgent(ctx, gen.UpsertHostAgentParams{
			HostID: a.HostID, OrgID: a.OrgID, AgentName: a.AgentName, CertSerial: a.CertSerial, Version: a.Version,
		}); err != nil {
			return fmt.Errorf("upsert agent: %w", err)
		}
		if err := s.q.RevokeAgentCertificates(ctx, gen.RevokeAgentCertificatesParams{OrgID: a.OrgID, HostID: a.HostID}); err != nil {
			return fmt.Errorf("revoke previous agent certificate: %w", err)
		}
		if err := s.q.InsertAgentCertificate(ctx, gen.InsertAgentCertificateParams{Serial: a.CertSerial, OrgID: a.OrgID, HostID: a.HostID}); err != nil {
			return fmt.Errorf("insert agent certificate: %w", err)
		}
		return nil
	})
}

// GetBySerial resolves a presented serial without any organization filter:
// the gRPC interceptor has no org context, and the serial is the primary
// key of agent_certificates.
func (s *SQLStore) GetBySerial(ctx context.Context, serial string) (AgentIdentity, error) {
	row, err := s.q.LookupAgentSerial(ctx, serial)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, ErrNotFound
	}
	if err != nil {
		return AgentIdentity{}, err
	}
	h := row.HostAgent
	return AgentIdentity{
		OrgID: h.OrgID, HostID: h.HostID, Name: h.AgentName, Version: h.Version,
		Revoked: row.CertRevokedAt.Valid, LastSeen: db.TimePtr(h.LastSeen),
	}, nil
}

func (s *SQLStore) Touch(ctx context.Context, serial, version string) error {
	return s.q.TouchAgentBySerial(ctx, gen.TouchAgentBySerialParams{Serial: serial, Version: version})
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Agent, error) {
	rows, err := s.q.ListHostAgents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	out := make([]Agent, len(rows))
	for i, r := range rows {
		out[i] = Agent{
			HostID: r.HostID, OrgID: r.OrgID, AgentName: r.AgentName, CertSerial: r.CertSerial,
			LastSeen: db.TimePtr(r.LastSeen), Version: r.Version, CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// Revoke marks every certificate of the host's agent revoked and removes
// the host_agents row, in one transaction bound to the organization.
func (s *SQLStore) Revoke(ctx context.Context, orgID, hostID uuid.UUID) error {
	return s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.q.RevokeAgentCertificates(ctx, gen.RevokeAgentCertificatesParams{OrgID: orgID, HostID: hostID}); err != nil {
			return fmt.Errorf("revoke agent certificates: %w", err)
		}
		n, err := s.q.DeleteHostAgent(ctx, gen.DeleteHostAgentParams{OrgID: orgID, HostID: hostID})
		if err != nil {
			return fmt.Errorf("delete agent: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RecordFacts merges the reported facts into hosts.facts and re-applies
// the automatic tags, in one org-bound transaction (the service contract
// is on Store.RecordFacts; the merge/tag semantics are documented there
// and on the queries themselves).
func (s *SQLStore) RecordFacts(ctx context.Context, orgID, hostID uuid.UUID, facts map[string]string) error {
	return s.d.WithWriteTx(ctx, orgID, func(ctx context.Context) error {
		payload, err := json.Marshal(facts)
		if err != nil {
			return fmt.Errorf("encode facts: %w", err)
		}
		n, err := s.q.UpsertHostFacts(ctx, gen.UpsertHostFactsParams{
			OrgID: orgID, HostID: hostID, Facts: payload,
		})
		if err != nil {
			return fmt.Errorf("merge host facts: %w", err)
		}
		if n == 0 {
			// The host does not exist in this organization (or belongs
			// to another one) — invisible, not forbidden, the same
			// contract hosts.GetHost carries.
			return ErrNotFound
		}
		for _, key := range factTagKeys {
			value, ok := facts[key]
			if !ok || value == "" {
				continue // no fact, no tag (and no deletion: the merge never removes)
			}
			if err := s.q.UpsertFactTag(ctx, gen.UpsertFactTagParams{
				OrgID: orgID, HostID: hostID, Key: key, Value: value,
			}); err != nil {
				return fmt.Errorf("upsert fact tag: %w", err)
			}
		}
		return nil
	})
}

func toToken(r gen.AgentEnrollment) Token {
	return Token{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, HostID: db.UUIDPtr(r.HostID),
		NameHint: derefText(r.NameHint), MaxUses: int(r.MaxUses), Uses: int(r.Uses),
		ExpiresAt: r.ExpiresAt, UsedAt: db.TimePtr(r.UsedAt),
		CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt,
	}
}

// nullText / derefText convert between the service's plain string (empty =
// unset) and the nullable name_hint column.
func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
