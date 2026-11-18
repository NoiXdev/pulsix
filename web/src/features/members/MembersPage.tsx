import { useMemo, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm } from 'react-hook-form';
import { Copy, UserPlus } from 'lucide-react';
import { DataList } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { copyText } from '../../lib/clipboard.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useInviteMember, useMembers } from '../../api/hooks/useMembers.ts';
import { schemas, type InviteFormValues } from '../../lib/schemas.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { PendingInvites } from './PendingInvites.tsx';
import { roleOptions } from './roles.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { MEMBER_ROLES, MEMBER_STATUSES, MEMBERS_FILTERS, filterMembers } from './membersSearch.ts';
import { memberColumns } from './memberColumns.tsx';
import { useMemberListActions } from './useMemberListActions.ts';
import { PageHeader } from '../../ui/settings.tsx';

/** An invitation cannot grant ownership. */
const INVITE_ROLES = ['viewer', 'operator', 'admin'] as const;

/**
 * MembersPage follows design/mockups/build.mjs's MEMBERS / ACL artboard's
 * member table and invite dialog. The mockup shows pending invitations
 * inline in the member list; they sit in their own table below it instead
 * (PendingInvites, backlog 7.29), since an invitation has none of a
 * member's columns and one action of its own.
 *
 * A row opens the member page (MemberPage, backlog 7.74), where the role
 * is changed; for a role that may manage members its menu also removes
 * the member.
 */
export function MembersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();

  const members = useMembers(org);
  const canManage = may(useOrg(org).data?.org.role, 'members.manage');
  const invite = useInviteMember(org);

  const [inviteOpen, setInviteOpen] = useState(false);
  // Set when an invitation was created but not e-mailed because the
  // instance has no outgoing mail configured: the link is shown for the
  // administrator to hand over instead.
  const [inviteLink, setInviteLink] = useState<{ email: string; url: string } | null>(null);

  const t = useT();
  const fmt = useFormat();
  const { inviteFormSchema } = useMemo(() => schemas(t), [t]);
  const inviteForm = useForm<InviteFormValues>({
    resolver: zodResolver(inviteFormSchema),
    defaultValues: { email: '', role: 'operator' },
  });

  const columns = memberColumns(t, fmt);
  const { filters, active, setFilter, reset } = useListFilters(MEMBERS_FILTERS);
  const view = useListView('members', columns, { mode: 'client', rowKey: (m) => m.user_id, resetKey: JSON.stringify(filters) });
  const rowActions = useMemberListActions(org, canManage);

  const membersGuard = useQueryGuard(members);
  if (membersGuard) return membersGuard;
  const all = members.data?.members ?? [];
  const shown = filterMembers(all, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.members.filters;

  return (
    <>
      <PageHeader
        title={t.settings.tabs.members}
        actions={
          <Button variant="primary" onClick={() => setInviteOpen(true)}>
            <UserPlus size={15} /> {t.members.invite}
          </Button>
        }
      />
      <DataList
        label={t.members.label}
        columns={columns}
        rows={rows}
        rowKey={(m) => m.user_id}
        rowLink={(m) => ({ to: '/o/$org/settings/members/$userId', params: { org, userId: m.user_id }, label: m.display_name })}
        rowLabel={(m) => `${m.display_name} (${m.email})`}
        rowActions={rowActions}
        loading={!view.ready}
        empty={active ? t.members.noMatch : null}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={all.length} noun={t.members.count} /> : undefined}>
            <SearchInput
              label={t.members.search}
              placeholder={t.members.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.role}
              allLabel={f.allRoles}
              options={roleOptions(t, MEMBER_ROLES)}
              value={filters.role}
              onChange={(v) => setFilter('role', v as typeof filters.role)}
            />
            <FilterSelect
              label={f.status}
              allLabel={f.anyStatus}
              options={MEMBER_STATUSES.map((v) => ({ value: v, label: v === 'active' ? t.members.active : t.members.disabled }))}
              value={filters.status}
              onChange={(v) => setFilter('status', v as typeof filters.status)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.members.count)}
      />

      <PendingInvites org={org} />

      <Dialog open={inviteOpen} onClose={() => setInviteOpen(false)} title={t.members.invite}>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={inviteForm.handleSubmit(async (values) => {
            try {
              const res = await invite.mutateAsync(values);
              if (res.mail_sent || !res.accept_url) {
                toast.show(t.members.inviteDialog.sent(values.email));
              } else {
                setInviteLink({ email: res.email, url: res.accept_url });
              }
              setInviteOpen(false);
              inviteForm.reset();
            } catch (err) {
              toast.show(err instanceof Error ? err.message : t.members.inviteDialog.failed, 'destructive');
            }
          })}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="invite_email">{t.members.inviteDialog.emailLabel}</Label>
            <Input id="invite_email" type="email" {...inviteForm.register('email')} placeholder={t.members.inviteDialog.emailPlaceholder} />
            {inviteForm.formState.errors.email && (
              <span className="text-[11px] text-destructive">{inviteForm.formState.errors.email.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="invite_role">{t.members.inviteDialog.roleLabel}</Label>
            <Controller
              control={inviteForm.control}
              name="role"
              render={({ field }) => (
                <Select
                  id="invite_role"
                  options={roleOptions(t, INVITE_ROLES)}
                  value={field.value}
                  onChange={field.onChange}
                />
              )}
            />
          </div>
          <p className="text-[11px] text-muted-foreground">{t.members.inviteDialog.ownerNote}</p>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setInviteOpen(false)}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={invite.isPending}>
              {t.members.inviteDialog.send}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={!!inviteLink} onClose={() => setInviteLink(null)} title={t.members.shareLink.title}>
        {inviteLink && (
          <div className="flex flex-col gap-4">
            <p className="text-xs text-muted-foreground">
              {t.members.shareLink.before} <span className="font-semibold text-foreground">{inviteLink.email}</span>{' '}
              {t.members.shareLink.after}
            </p>
            <code
              aria-label={t.members.shareLink.linkLabel}
              className="break-all rounded-md border border-border bg-background p-2 font-mono text-[11px]"
            >
              {inviteLink.url}
            </code>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                onClick={async () => {
                  const ok = await copyText(inviteLink.url);
                  if (ok) toast.show(t.members.shareLink.copied);
                  else toast.show(t.members.shareLink.copyFailed, 'destructive');
                }}
              >
                <Copy size={14} /> {t.members.shareLink.copy}
              </Button>
              <Button type="button" variant="primary" onClick={() => setInviteLink(null)}>
                {t.members.shareLink.done}
              </Button>
            </div>
          </div>
        )}
      </Dialog>

    </>
  );
}
