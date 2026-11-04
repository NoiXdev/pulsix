package tenancy

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries, mirroring auth.SQLStore's
// seam: the service never imports gen or pgx directly, so it can be driven
// from the in-memory fake in service_test.go as well as the real database.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool: *db.DB
// runs each statement inside the organization bound to the request context
// (db.WithOrgScope), which is what makes row-level security enforce the
// tenant boundary for memberships and invitations independently of each
// query's own WHERE clause.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// uniqueViolationCode is the PostgreSQL error code for a unique constraint
// violation (organizations.slug here).
const uniqueViolationCode = "23505"

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

func (s *SQLStore) CreateOrg(ctx context.Context, o Org) error {
	_, err := s.q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: o.ID, Slug: o.Slug, Name: o.Name})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrSlugTaken
		}
		return err
	}
	return nil
}

func (s *SQLStore) GetOrgBySlug(ctx context.Context, slug string) (Org, error) {
	row, err := s.q.GetOrganizationBySlug(ctx, slug)
	if err != nil {
		return Org{}, mapErr(err)
	}
	return toOrg(row), nil
}

func (s *SQLStore) GetOrgByID(ctx context.Context, id uuid.UUID) (Org, error) {
	row, err := s.q.GetOrganizationByID(ctx, id)
	if err != nil {
		return Org{}, mapErr(err)
	}
	return toOrg(row), nil
}

func (s *SQLStore) UpdateOrg(ctx context.Context, o Org) error {
	_, err := s.q.UpdateOrganization(ctx, gen.UpdateOrganizationParams{ID: o.ID, Name: o.Name})
	return mapErr(err)
}

func toOrg(r gen.Organization) Org {
	return Org{ID: r.ID, Slug: r.Slug, Name: r.Name, CreatedAt: r.CreatedAt}
}

func (s *SQLStore) ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]OrgMembership, error) {
	rows, err := s.q.ListMembershipsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]OrgMembership, len(rows))
	for i, r := range rows {
		out[i] = OrgMembership{OrgID: r.OrgID, Slug: r.Slug, Name: r.Name, Role: Role(r.Role), CreatedAt: r.CreatedAt}
	}
	return out, nil
}

func (s *SQLStore) GetMembership(ctx context.Context, orgID, userID uuid.UUID) (Role, error) {
	row, err := s.q.GetMembership(ctx, gen.GetMembershipParams{OrgID: orgID, UserID: userID})
	if err != nil {
		return "", mapErr(err)
	}
	return Role(row.Role), nil
}

func (s *SQLStore) AddMembership(ctx context.Context, orgID, userID uuid.UUID, role Role) error {
	return s.q.AddMembership(ctx, gen.AddMembershipParams{OrgID: orgID, UserID: userID, Role: gen.OrgRole(role)})
}

// UpdateMembershipRole returns ErrNotFound when no row was affected — either
// because no matching membership existed, or because the query's own
// atomic last-owner guard refused the change (see tenancy_users.sql). The
// service (ChangeRole) is the one that knows which of those two it is,
// since it already looked the target's current membership up before
// calling this; the store itself makes no attempt to tell them apart.
func (s *SQLStore) UpdateMembershipRole(ctx context.Context, orgID, userID uuid.UUID, role Role) error {
	n, err := s.q.UpdateMembershipRole(ctx, gen.UpdateMembershipRoleParams{OrgID: orgID, UserID: userID, Role: gen.OrgRole(role)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMembership returns ErrNotFound when no matching row existed, so a
// caller can never mistake "nothing happened" for success — the same
// guarantee SQLTokenStore.Revoke gives. As with UpdateMembershipRole above,
// this also covers the query's own atomic last-owner guard refusing the
// delete (see tenancy_users.sql); RemoveMember is what tells the two apart.
func (s *SQLStore) DeleteMembership(ctx context.Context, orgID, userID uuid.UUID) error {
	n, err := s.q.DeleteMembership(ctx, gen.DeleteMembershipParams{OrgID: orgID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) CountOwners(ctx context.Context, orgID uuid.UUID) (int, error) {
	n, err := s.q.CountOwners(ctx, orgID)
	return int(n), err
}

func (s *SQLStore) ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := s.q.ListMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, r := range rows {
		out[i] = Member{
			UserID: r.UserID, Email: r.Email, DisplayName: r.DisplayName, Role: Role(r.Role),
			DisabledAt: fromPgTimestamptz(r.DisabledAt), CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

func (s *SQLStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, mapErr(err)
	}
	return User{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, PasswordHash: row.PasswordHash}, nil
}

func (s *SQLStore) CreateUser(ctx context.Context, u User) error {
	_, err := s.q.CreateUser(ctx, gen.CreateUserParams{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, PasswordHash: u.PasswordHash})
	return err
}

func (s *SQLStore) SetUserPasswordAndName(ctx context.Context, id uuid.UUID, hash, name string) error {
	return s.q.SetUserPasswordAndName(ctx, gen.SetUserPasswordAndNameParams{ID: id, PasswordHash: &hash, DisplayName: name})
}

// DisableUser returns ErrNotFound for an unknown id and ErrLastInstanceAdmin
// when the query's own atomic guard refused to disable the last active
// instance administrator (see tenancy_users.sql). The guard is what decides;
// when no row came back, the follow-up lookup only tells its outcomes
// apart: an unknown id, an account that was already disabled (left as it
// is, and so not active before), or the guard's refusal.
func (s *SQLStore) DisableUser(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	wasActive, err := s.q.DisableUser(ctx, gen.DisableUserParams{ID: id, DisabledAt: pgtype.Timestamptz{Time: at, Valid: true}})
	if err == nil {
		return wasActive, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return false, mapErr(err)
	}
	if u.DisabledAt.Valid {
		return false, nil
	}
	return false, ErrLastInstanceAdmin
}

func (s *SQLStore) RevokeSessionsForUser(ctx context.Context, id uuid.UUID) error {
	return s.q.RevokeSessionsForUser(ctx, id)
}

func (s *SQLStore) RevokeTokensForUser(ctx context.Context, id uuid.UUID) error {
	return s.q.RevokeTokensForUser(ctx, id)
}

func (s *SQLStore) InsertInvite(ctx context.Context, inv Invite, tokenHash []byte) error {
	return s.q.InsertInvite(ctx, gen.InsertInviteParams{
		ID: inv.ID, OrgID: inv.OrgID, Email: inv.Email, Role: gen.OrgRole(inv.Role),
		TokenHash: tokenHash, InvitedBy: inv.InvitedBy, ExpiresAt: inv.ExpiresAt,
	})
}

func (s *SQLStore) DeleteInvite(ctx context.Context, orgID, id uuid.UUID) error {
	return s.q.DeleteInvite(ctx, gen.DeleteInviteParams{ID: id, OrgID: orgID})
}

func (s *SQLStore) PeekInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error) {
	row, err := s.q.PeekInvite(ctx, gen.PeekInviteParams{TokenHash: tokenHash, Now: now, IssuerRoles: inviteIssuerRoles()})
	if err != nil {
		return Invite{}, mapErr(err)
	}
	return Invite{
		ID: row.ID, OrgID: row.OrgID, Email: row.Email, Role: Role(row.Role),
		InvitedBy: row.InvitedBy, ExpiresAt: row.ExpiresAt,
	}, nil
}

func (s *SQLStore) TakeInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error) {
	row, err := s.q.TakeInvite(ctx, gen.TakeInviteParams{TokenHash: tokenHash, Now: now, IssuerRoles: inviteIssuerRoles()})
	if err != nil {
		return Invite{}, mapErr(err)
	}
	return Invite{
		ID: row.ID, OrgID: row.OrgID, Email: row.Email, Role: Role(row.Role),
		InvitedBy: row.InvitedBy, ExpiresAt: row.ExpiresAt,
	}, nil
}

func (s *SQLStore) ListPendingInvites(ctx context.Context, orgID uuid.UUID, now time.Time) ([]PendingInvite, error) {
	rows, err := s.q.ListPendingInvites(ctx, gen.ListPendingInvitesParams{OrgID: orgID, Now: now, IssuerRoles: inviteIssuerRoles()})
	if err != nil {
		return nil, err
	}
	out := make([]PendingInvite, len(rows))
	for i, r := range rows {
		out[i] = PendingInvite{
			ID: r.ID, Email: r.Email, Role: Role(r.Role), InvitedBy: r.InvitedBy,
			InvitedByName: r.InvitedByName, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

func (s *SQLStore) RevokeInvite(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.RevokeInvite(ctx, gen.RevokeInviteParams{ID: id, OrgID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// inviteIssuerRoles are the roles an invitation's issuer must still hold
// for it to be accepted (backlog 7.29): those that may manage members,
// read from the permission matrix rather than repeated in SQL.
func inviteIssuerRoles() []string {
	var out []string
	for role := range rank {
		if Allows(role, PermMembersManage) {
			out = append(out, string(role))
		}
	}
	return out
}

func fromPgTimestamptz(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
