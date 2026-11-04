import { Link, useParams } from '@tanstack/react-router';
import { DetailLayout, Section, SidebarFacts } from '../../ui/detail-layout.tsx';
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
 * who it is in the sidebar, then its organizations (read-only; roles are
 * changed per organization), how it signs in, and the account actions.
 * GET /admin/users/{userId} admits instance administrators only, so anyone
 * else sees the access-denied page.
 */
export function AccountPage() {
  const { userId } = useParams({ strict: false }) as { userId: string };
  const t = useT();
  const account = useAdminUser(userId);
  const me = useMe({ enabled: true });

  const guard = useQueryGuard(account);
  if (guard) return guard;
  if (!account.data) return <QueryLoading />;
  const user = account.data;

  return (
    <DetailLayout
      sidebar={<AccountSidebar user={user} />}
      nav={[
        { id: 'organizations', label: t.users.account.organizations, count: (user.memberships ?? []).length },
        { id: 'sign-in', label: t.users.signIn.title },
        { id: 'actions', label: t.users.actions.title },
      ]}
    >
      <Section id="organizations" title={t.users.account.organizations}>
        <AccountOrganizations user={user} />
      </Section>
      <Section id="sign-in" title={t.users.signIn.title}>
        <AccountSignIn user={user} />
      </Section>
      <Section id="actions" title={t.users.actions.title}>
        {me.data ? <AccountActions key={user.id} user={user} selfId={me.data.user.id} /> : <QueryLoading />}
      </Section>
    </DetailLayout>
  );
}

function AccountSidebar({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const fmt = useFormat();
  const f = t.users.account;
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-xl font-semibold break-words">{user.display_name}</h1>
      <SidebarFacts
        items={[
          { label: f.email, value: user.email },
          { label: f.state, value: <Pill tone={accountStateTone(user.state)}>{accountStateLabel(t, user.state)}</Pill> },
          { label: f.instanceAdmin, value: user.instance_admin ? f.yes : f.no },
          { label: f.created, value: fmt.date(user.created_at) },
          { label: f.lastSignIn, value: user.last_sign_in_at ? fmt.dateTime(user.last_sign_in_at) : t.users.never },
        ]}
      />
    </div>
  );
}

/** The account's memberships. An organization the administrator belongs to
 * as well links to its member page, where the role is changed; another one
 * cannot be opened (no "join as admin"). */
function AccountOrganizations({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const memberships = user.memberships ?? [];
  if (memberships.length === 0) {
    return <p className="text-xs text-muted-foreground">{t.users.account.noOrgs}</p>;
  }
  return (
    <>
      <div className="flex flex-col rounded-[10px] border border-border bg-card">
        {memberships.map((m) => (
          <div key={m.org_id} className="flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
            <div className="flex min-w-0 flex-1 flex-col">
              {m.actor_is_member ? (
                <Link
                  to="/o/$org/settings/members/$userId"
                  params={{ org: m.slug, userId: user.id }}
                  aria-label={t.users.account.openMember(m.name)}
                  className="truncate text-[13px] font-semibold hover:underline"
                >
                  {m.name}
                </Link>
              ) : (
                <span className="truncate text-[13px] font-semibold">{m.name}</span>
              )}
              <span className="truncate font-mono text-[11px] text-muted-foreground">{m.slug}</span>
            </div>
            <RoleBadge role={m.role} />
          </div>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">{t.users.account.orgsHint}</p>
    </>
  );
}
