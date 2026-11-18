import { Link, useParams } from '@tanstack/react-router';
import { SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminUser } from '../../api/hooks/useAdminUsers.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import type { AdminUserDetail } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { AccountActions } from './AccountActions.tsx';
import { AccountSignIn } from './AccountSignIn.tsx';
import { accountStateLabel, accountStateTone } from './accountState.ts';

/**
 * AccountPage is one account on the instance, for instance administrators:
 * who it is, then its organizations (read-only; roles are
 * changed per organization), how it signs in, and the account actions.
 * GET /admin/users/{userId} admits instance administrators only, so anyone
 * else sees the access-denied page.
 */
export function AccountPage() {
  const { userId } = useParams({ strict: false }) as { userId: string };
  const account = useAdminUser(userId);
  const me = useMe({ enabled: true });

  const guard = useQueryGuard(account);
  if (guard) return guard;
  if (!account.data) return <QueryLoading />;
  const user = account.data;

  return (
    <SettingsPage title={user.display_name}>
      <AccountFacts user={user} />
      <AccountOrganizations user={user} />
      <AccountSignIn user={user} />
      {me.data ? <AccountActions key={user.id} user={user} selfId={me.data.user.id} /> : <QueryLoading />}
    </SettingsPage>
  );
}

function AccountFacts({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const fmt = useFormat();
  const f = t.users.account;
  return (
    <SettingsCard title={f.card} description={f.cardDescription}>
      <SettingsRow label={f.email}>{user.email}</SettingsRow>
      <SettingsRow label={f.state}>
        <Pill tone={accountStateTone(user.state)}>{accountStateLabel(t, user.state)}</Pill>
      </SettingsRow>
      <SettingsRow label={f.instanceAdmin}>{user.instance_admin ? f.yes : f.no}</SettingsRow>
      <SettingsRow label={f.created}>{fmt.date(user.created_at)}</SettingsRow>
      <SettingsRow label={f.lastSignIn}>{user.last_sign_in_at ? fmt.dateTime(user.last_sign_in_at) : t.users.never}</SettingsRow>
    </SettingsCard>
  );
}

/** The account's memberships. An organization the administrator belongs to
 * as well links to its member page, where the role is changed; another one
 * cannot be opened (no "join as admin"). */
function AccountOrganizations({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const memberships = user.memberships ?? [];
  return (
    <SettingsCard title={t.users.account.organizations} description={t.users.account.orgsHint}>
      {memberships.length === 0 && <SettingsRow label={t.users.account.noOrgs} />}
      {memberships.map((m) => (
        <SettingsRow
          key={m.org_id}
          label={
            m.actor_is_member ? (
              <Link
                to="/o/$org/settings/members/$userId"
                params={{ org: m.slug, userId: user.id }}
                aria-label={t.users.account.openMember(m.name)}
                className="hover:underline"
              >
                {m.name}
              </Link>
            ) : (
              m.name
            )
          }
          help={<span className="font-mono">{m.slug}</span>}
        >
          <RoleBadge role={m.role} />
        </SettingsRow>
      ))}
    </SettingsCard>
  );
}
