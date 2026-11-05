import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { DetailLayout, Section, SidebarFacts, type SectionNavItem } from '../../ui/detail-layout.tsx';
import { Label } from '../../ui/label.tsx';
import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { Select } from '../../ui/Select.tsx';
import { QueryError, QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { ApiError } from '../../api/client.ts';
import { useChangeMemberRole, useMembers, useRemoveMember } from '../../api/hooks/useMembers.ts';
import { useAdminUser } from '../../api/hooks/useAdminUsers.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { Member } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';
import { AccountActions } from '../users/AccountActions.tsx';
import { AccountSignIn } from '../users/AccountSignIn.tsx';
import { roleOptions } from './roles.ts';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;

/**
 * MemberPage is one member of this organization, opened from a row on
 * Members. It replaces the role dialog: a role that may manage members
 * changes the role or removes the member here, under the server's own
 * rules (the last owner stays, only an owner touches an owner), whose
 * refusals are shown as they come.
 *
 * For an instance administrator it also shows the account's Sign-in and
 * Actions sections, the same components as the account page. Nobody else
 * sees them, and for nobody else is the account loaded at all.
 */
export function MemberPage() {
  const { org, userId } = useParams({ strict: false }) as { org: string; userId: string };
  const t = useT();
  const members = useMembers(org);
  const me = useMe({ enabled: true });
  const instanceAdmin = me.data?.instance_admin === true;
  const account = useAdminUser(userId, { enabled: instanceAdmin });

  const guard = useQueryGuard(members);
  if (guard) return guard;
  if (!members.data) return <QueryLoading />;
  const member = (members.data.members ?? []).find((m) => m.user_id === userId);
  if (!member) return <QueryError error={new ApiError(404, null)} />;

  const nav: SectionNavItem[] = [{ id: 'role', label: t.members.page.roleTitle }];
  if (instanceAdmin) {
    nav.push({ id: 'sign-in', label: t.users.signIn.title }, { id: 'actions', label: t.users.actions.title });
  }

  return (
    <DetailLayout sidebar={<MemberSidebar member={member} />} nav={nav}>
      <Section id="role" title={t.members.page.roleTitle}>
        <MemberRole org={org} member={member} />
      </Section>
      {instanceAdmin && (
        <>
          <Section id="sign-in" title={t.users.signIn.title}>
            {account.data ? <AccountSignIn user={account.data} /> : <AccountPending error={account.error} />}
          </Section>
          <Section id="actions" title={t.users.actions.title}>
            {account.data && me.data ? (
              <AccountActions key={account.data.id} user={account.data} selfId={me.data.user.id} />
            ) : (
              <AccountPending error={account.error} />
            )}
          </Section>
        </>
      )}
    </DetailLayout>
  );
}

function AccountPending({ error }: { error: unknown }) {
  if (error instanceof Error) return <p className="text-xs text-destructive">{error.message}</p>;
  return <QueryLoading />;
}

function MemberSidebar({ member }: { member: Member }) {
  const t = useT();
  const fmt = useFormat();
  const p = t.members.page;
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-xl font-semibold break-words">{member.display_name}</h1>
      <SidebarFacts
        items={[
          { label: p.email, value: member.email },
          { label: p.joined, value: fmt.date(member.created_at) },
          {
            label: p.status,
            value: member.disabled_at ? (
              <Pill tone="unknown">{t.members.disabled}</Pill>
            ) : (
              <Pill tone="ok">{t.members.active}</Pill>
            ),
          },
        ]}
      />
    </div>
  );
}

/** The member's role, and for a role that may manage members the controls
 * to change it or remove the member. */
function MemberRole({ org, member }: { org: string; member: Member }) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const orgInfo = useOrg(org);
  const actorRole = orgInfo.data?.org.role;
  const changeRole = useChangeMemberRole(org);
  const removeMember = useRemoveMember(org);
  const [role, setRole] = useState(member.role);
  const p = t.members.page;

  if (!orgInfo.data) return <QueryLoading />;
  if (!may(actorRole, 'members.manage')) {
    return (
      <div className="flex flex-col items-start gap-2">
        <RoleBadge role={member.role} />
        <p className="text-xs text-muted-foreground">{p.readOnly}</p>
      </div>
    );
  }

  async function saveRole() {
    try {
      await changeRole.mutateAsync({ userId: member.user_id, role });
      toast.show(p.updated(roleLabel(t, role)));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : p.failed, 'destructive');
    }
  }

  async function remove() {
    if (!confirm(t.members.confirmRemove(member.display_name))) return;
    try {
      await removeMember.mutateAsync(member.user_id);
      toast.show(t.members.removed(member.display_name));
      void navigate({ to: '/o/$org/settings/members', params: { org } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.members.removeFailed, 'destructive');
    }
  }

  return (
    <div className="flex flex-col gap-5 rounded-[10px] border border-border bg-card px-4 py-4">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="member-role">{p.roleLabel}</Label>
        <div className="flex items-center gap-2">
          <div className="w-48">
            <Select id="member-role" options={roleOptions(t, ROLES)} value={role} onChange={setRole} />
          </div>
          <Button
            type="button"
            variant="primary"
            disabled={role === member.role || changeRole.isPending}
            onClick={() => void saveRole()}
          >
            {p.changeRole}
          </Button>
        </div>
      </div>
      <div className="flex flex-col items-start gap-2 border-t border-border pt-4">
        <p className="text-xs text-muted-foreground">{p.removeHint}</p>
        <Button type="button" variant="destructive" disabled={removeMember.isPending} onClick={() => void remove()}>
          {p.removeButton}
        </Button>
      </div>
    </div>
  );
}
