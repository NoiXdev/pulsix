import type { AdminUserDetail } from '../../api/types.ts';

/** An account as GET /admin/users/{userId} answers, for the users and
 * member page tests. */
export function accountDetail(over: Partial<AdminUserDetail> = {}): AdminUserDetail {
  return {
    id: 'u2',
    email: 'sara@acme.io',
    display_name: 'Sara K.',
    instance_admin: false,
    state: 'active',
    org_count: 1,
    last_sign_in_at: '2026-10-30T08:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    disabled_at: null,
    must_change_password: false,
    invite_pending: false,
    temp_password_expires_at: null,
    memberships: [{ org_id: 'o1', slug: 'acme', name: 'Acme', role: 'viewer', actor_is_member: true }],
    factors: { totp: true, passkeys: 2, recovery_codes_left: 7 },
    active_sessions: 3,
    ...over,
  };
}
