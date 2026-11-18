import { useNavigate, useParams } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { CardSaveBar, SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { useCardForm } from '../../ui/useCardForm.ts';
import { ReadOnlyNote } from '../settings/ReadOnlyNote.tsx';
import { DangerZone } from '../users/DangerZone.tsx';
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
import { useConfirm } from '../../ui/confirmContext.ts';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;

/**
 * MemberPage is one member of this organization, opened from a row on
 * Members. It replaces the role dialog: a role that may manage members
 * changes the role or removes the member here, under the server's own
 * rules (the last owner stays, only an owner touches an owner), whose
 * refusals are shown as they come.
 *
 * For an instance administrator it also shows the account's Sign-in,
 * Actions and Danger zone cards, the same components as the account page
 * (the removal joins the account's destructive actions in one Danger zone). Nobody else
 * sees them, and for nobody else is the account loaded at all.
 */
export function MemberPage() {
  const { org, userId } = useParams({ strict: false }) as { org: string; userId: string };
  const t = useT();
  const members = useMembers(org);
  const me = useMe({ enabled: true });
  const instanceAdmin = me.data?.instance_admin === true;
  const account = useAdminUser(userId, { enabled: instanceAdmin });
  const orgInfo = useOrg(org);

  const guard = useQueryGuard(members);
  if (guard) return guard;
  if (!members.data) return <QueryLoading />;
  const member = (members.data.members ?? []).find((m) => m.user_id === userId);
  if (!member) return <QueryError error={new ApiError(404, null)} />;

  const canManage = may(orgInfo.data?.org.role, 'members.manage');
  const removeRow = canManage ? <RemoveMemberRow org={org} member={member} /> : null;
  const accountReady = account.data && me.data;

  return (
    <SettingsPage title={member.display_name}>
      <MemberFacts member={member} />
      <MemberRole org={org} member={member} canManage={canManage} loaded={!!orgInfo.data} />
      {instanceAdmin && (
        <>
          {account.data ? <AccountSignIn user={account.data} /> : <AccountPending error={account.error} title={t.users.signIn.title} />}
          {accountReady ? (
            <AccountActions key={account.data.id} user={account.data} selfId={me.data.user.id} dangerExtra={removeRow} />
          ) : (
            <AccountPending error={account.error} title={t.users.actions.title} />
          )}
        </>
      )}
      {removeRow && !(instanceAdmin && accountReady) && <DangerZone>{removeRow}</DangerZone>}
    </SettingsPage>
  );
}

/** A card standing in for an account section while the account loads. */
function AccountPending({ error, title }: { error: unknown; title: string }) {
  return (
    <SettingsCard title={title}>
      <div className="px-5 pb-4">
        {error instanceof Error ? <p className="text-xs text-destructive">{error.message}</p> : <QueryLoading />}
      </div>
    </SettingsCard>
  );
}

function MemberFacts({ member }: { member: Member }) {
  const t = useT();
  const fmt = useFormat();
  const p = t.members.page;
  return (
    <SettingsCard title={p.accountTitle} description={p.accountDescription}>
      <SettingsRow label={p.email}>{member.email}</SettingsRow>
      <SettingsRow label={p.joined}>{fmt.date(member.created_at)}</SettingsRow>
      <SettingsRow label={p.status}>
        {member.disabled_at ? <Pill tone="unknown">{t.members.disabled}</Pill> : <Pill tone="ok">{t.members.active}</Pill>}
      </SettingsRow>
    </SettingsCard>
  );
}

/** The member's role: a role that may manage members changes it here, with
 * the card's own save bar; anyone else sees it read-only. */
function MemberRole({ org, member, canManage, loaded }: { org: string; member: Member; canManage: boolean; loaded: boolean }) {
  const t = useT();
  const toast = useToast();
  const changeRole = useChangeMemberRole(org);
  const form = useCardForm<string>(member.role);
  const p = t.members.page;

  if (!loaded) {
    return (
      <SettingsCard title={p.roleTitle} description={p.roleDescription}>
        <div className="px-5 pb-4">
          <QueryLoading />
        </div>
      </SettingsCard>
    );
  }
  if (!canManage) {
    return (
      <SettingsCard title={p.roleTitle} description={p.roleDescription} footer={<ReadOnlyNote>{p.readOnly}</ReadOnlyNote>}>
        <SettingsRow label={p.roleLabel}>
          <RoleBadge role={member.role} />
        </SettingsRow>
      </SettingsCard>
    );
  }

  async function saveRole(e: React.FormEvent) {
    e.preventDefault();
    if (!form.dirty) return;
    const role = form.draft;
    try {
      await changeRole.mutateAsync({ userId: member.user_id, role });
      form.markSaved(role);
      toast.show(p.updated(roleLabel(t, role)));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : p.failed, 'destructive');
    }
  }

  return (
    <form id="member-role-form" onSubmit={(e) => void saveRole(e)}>
      <SettingsCard
        title={p.roleTitle}
        description={p.roleDescription}
        footer={
          <CardSaveBar
            formId="member-role-form"
            dirty={form.dirty}
            saving={changeRole.isPending}
            onDiscard={form.discard}
            saveLabel={p.changeRole}
          />
        }
      >
        <SettingsRow label={p.roleLabel} htmlFor="member-role">
          <div className="w-48">
            <Select id="member-role" portal options={roleOptions(t, ROLES)} value={form.draft} onChange={form.setDraft} />
          </div>
        </SettingsRow>
      </SettingsCard>
    </form>
  );
}

/** The Danger zone row that removes the member after a confirmation. */
function RemoveMemberRow({ org, member }: { org: string; member: Member }) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const removeMember = useRemoveMember(org);
  const p = t.members.page;

  async function remove() {
    if (!(await ask({ message: t.members.confirmRemove(member.display_name), confirmLabel: t.common.remove, destructive: true }))) return;
    try {
      await removeMember.mutateAsync(member.user_id);
      toast.show(t.members.removed(member.display_name));
      void navigate({ to: '/o/$org/settings/members', params: { org } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.members.removeFailed, 'destructive');
    }
  }

  return (
    <SettingsRow label={p.removeButton} help={p.removeHint}>
      <Button type="button" variant="destructive" disabled={removeMember.isPending} onClick={() => void remove()}>
        {p.removeButton}
      </Button>
    </SettingsRow>
  );
}
