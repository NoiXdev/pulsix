import { useMemo, useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm } from 'react-hook-form';
import { Copy, UserPlus } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { ListRow, RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useInviteMember, useMembers, useRemoveMember } from '../../api/hooks/useMembers.ts';
import { schemas, type InviteFormValues } from '../../lib/schemas.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { PendingInvites } from './PendingInvites.tsx';

const COLUMNS = '36px minmax(0, 1fr) 130px 110px 90px 40px';

/**
 * MembersPage follows design/mockups/build.mjs's MEMBERS / ACL artboard's
 * member table and invite dialog. The mockup shows pending invitations
 * inline in the member list; they sit in their own table below it instead
 * (PendingInvites, backlog 7.29), since an invitation has none of a
 * member's columns and one action of its own.
 *
 * A row opens the member page (MemberPage, backlog 7.74), where the role
 * is changed; its menu also removes the member.
 */
export function MembersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const navigate = useNavigate();

  const members = useMembers(org);
  const invite = useInviteMember(org);
  const removeMember = useRemoveMember(org);

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

  async function remove(userId: string, name: string) {
    if (!confirm(t.members.confirmRemove(name))) return;
    try {
      await removeMember.mutateAsync(userId);
      toast.show(t.members.removed(name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.members.removeFailed, 'destructive');
    }
  }

  const membersGuard = useQueryGuard(members);
  if (membersGuard) return membersGuard;

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.settings.tabs.members}</h1>
        <Button variant="primary" onClick={() => setInviteOpen(true)}>
          <UserPlus size={15} /> {t.members.invite}
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">
        {members.data ? t.members.count((members.data.members ?? []).length) : t.common.loading}
      </span>

      <Card>
        <GridHeader columns={COLUMNS}>
          <span />
          <span>{t.members.columns.member}</span>
          <span>{t.members.columns.role}</span>
          <span>{t.members.columns.joined}</span>
          <span>{t.members.columns.status}</span>
          <span className="sr-only">{t.users.actions.title}</span>
        </GridHeader>
        {(members.data?.members ?? []).map((m) => (
          <ListRow
            key={m.user_id}
            to="/o/$org/settings/members/$userId"
            params={{ org, userId: m.user_id }}
            columns={COLUMNS}
            label={m.display_name}
            menu={
              <RowMenu label={t.members.rowActions(m.display_name)}>
                <RowMenuItem onSelect={() => void navigate({ to: '/o/$org/settings/members/$userId', params: { org, userId: m.user_id } })}>
                  {t.members.open}
                </RowMenuItem>
                <RowMenuItem destructive onSelect={() => void remove(m.user_id, m.display_name)}>
                  {t.members.removeItem}
                </RowMenuItem>
              </RowMenu>
            }
          >
            <span className="flex h-7 w-7 items-center justify-center rounded-full bg-brand-dim text-[11px] font-bold text-background">
              {m.display_name
                .split(' ')
                .slice(0, 2)
                .map((p) => p[0]?.toUpperCase())
                .join('')}
            </span>
            <div className="flex min-w-0 flex-col">
              <span className="truncate text-xs font-semibold">{m.display_name}</span>
              <span className="truncate text-[11px] text-muted-foreground">{m.email}</span>
            </div>
            <span>
              <RoleBadge role={m.role} />
            </span>
            <span className="text-[11px] text-muted-foreground">{fmt.date(m.created_at)}</span>
            <span>
              {m.disabled_at ? (
                <Pill tone="unknown">{t.members.disabled}</Pill>
              ) : (
                <Pill tone="ok">{t.members.active}</Pill>
              )}
            </span>
            <span />
          </ListRow>
        ))}
      </Card>

      <PendingInvites org={org} />

      <Dialog open={inviteOpen} onClose={() => setInviteOpen(false)} title={t.members.invite}>
        <form
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
                  options={[
                    { value: 'viewer', label: 'viewer' },
                    { value: 'operator', label: 'operator' },
                    { value: 'admin', label: 'admin' },
                  ]}
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
                  try {
                    await navigator.clipboard.writeText(inviteLink.url);
                    toast.show(t.members.shareLink.copied);
                  } catch {
                    toast.show(t.members.shareLink.copyFailed, 'destructive');
                  }
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
