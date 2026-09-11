import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { Copy, UserPlus } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Pill, RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { SettingsTabs } from '../../ui/SettingsTabs.tsx';
import { useChangeMemberRole, useInviteMember, useMembers, useRemoveMember } from '../../api/hooks/useMembers.ts';
import { inviteFormSchema, type InviteFormValues } from '../../lib/schemas.ts';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
const COLUMNS = '36px minmax(0, 1fr) 130px 110px 90px 90px';

/**
 * MembersPage follows design/mockups/build.mjs's MEMBERS / ACL artboard's
 * member table and invite dialog. The mockup also shows a combined
 * "Invitations" tab and pending-invite rows inline in the member list —
 * v0.1's API has no GET route that lists pending invitations (only
 * POST /invites to create one and POST /invites/accept to redeem it), so
 * that tab is left out here rather than built against data that does not
 * exist; see task-15a-report.md.
 */
export function MembersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();

  const members = useMembers(org);
  const invite = useInviteMember(org);
  const changeRole = useChangeMemberRole(org);
  const removeMember = useRemoveMember(org);

  const [inviteOpen, setInviteOpen] = useState(false);
  // Set when an invitation was created but not e-mailed because the
  // instance has no outgoing mail configured: the link is shown for the
  // administrator to hand over instead.
  const [inviteLink, setInviteLink] = useState<{ email: string; url: string } | null>(null);
  const [roleTarget, setRoleTarget] = useState<{ userId: string; name: string; role: string } | null>(null);

  const inviteForm = useForm<InviteFormValues>({
    resolver: zodResolver(inviteFormSchema),
    defaultValues: { email: '', role: 'operator' },
  });

  const membersGuard = useQueryGuard(members);
  if (membersGuard) return membersGuard;

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Members</h1>
          <SettingsTabs org={org} />
        </div>
        <Button variant="primary" onClick={() => setInviteOpen(true)}>
          <UserPlus size={15} /> Invite member
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">
        {members.data ? `${(members.data.members ?? []).length} members` : 'Loading…'}
      </span>

      <Card>
        <GridHeader columns={COLUMNS}>
          <span />
          <span>Member</span>
          <span>Role</span>
          <span>Joined</span>
          <span>Status</span>
          <span />
        </GridHeader>
        {(members.data?.members ?? []).map((m) => (
          <GridRow key={m.user_id} columns={COLUMNS}>
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
            <button
              type="button"
              className="w-fit"
              onClick={() => setRoleTarget({ userId: m.user_id, name: m.display_name, role: m.role })}
            >
              <RoleBadge role={m.role} />
            </button>
            <span className="text-[11px] text-muted-foreground">
              {new Date(m.created_at).toLocaleDateString()}
            </span>
            <span>{m.disabled_at ? <Pill tone="unknown">disabled</Pill> : <Pill tone="ok">active</Pill>}</span>
            <button
              type="button"
              className="text-[11px] text-destructive"
              onClick={async () => {
                if (!confirm(`Remove ${m.display_name} from the organization?`)) return;
                try {
                  await removeMember.mutateAsync(m.user_id);
                  toast.show(`${m.display_name} removed`);
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : 'failed to remove member', 'destructive');
                }
              }}
            >
              remove
            </button>
          </GridRow>
        ))}
      </Card>

      <Dialog open={inviteOpen} onClose={() => setInviteOpen(false)} title="Invite member">
        <form
          className="flex flex-col gap-4"
          onSubmit={inviteForm.handleSubmit(async (values) => {
            try {
              const res = await invite.mutateAsync(values);
              if (res.mail_sent || !res.accept_url) {
                toast.show(`Invitation sent to ${values.email}`);
              } else {
                setInviteLink({ email: res.email, url: res.accept_url });
              }
              setInviteOpen(false);
              inviteForm.reset();
            } catch (err) {
              toast.show(err instanceof Error ? err.message : 'failed to send invitation', 'destructive');
            }
          })}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="invite_email">E-mail address</Label>
            <Input id="invite_email" type="email" {...inviteForm.register('email')} placeholder="new.colleague@acme.io" />
            {inviteForm.formState.errors.email && (
              <span className="text-[11px] text-destructive">{inviteForm.formState.errors.email.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="invite_role">Role</Label>
            <select
              id="invite_role"
              {...inviteForm.register('role')}
              className="h-8 rounded-md border border-border bg-background px-2.5 text-xs"
            >
              <option value="viewer">viewer</option>
              <option value="operator">operator</option>
              <option value="admin">admin</option>
            </select>
          </div>
          <p className="text-[11px] text-muted-foreground">
            Owner role cannot be granted by invitation; promote after joining. The invitee sets a password and must
            enrol a second factor before entering the organization.
          </p>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setInviteOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={invite.isPending}>
              Send invitation
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={!!inviteLink} onClose={() => setInviteLink(null)} title="Share the invitation link">
        {inviteLink && (
          <div className="flex flex-col gap-4">
            <p className="text-xs text-muted-foreground">
              Outgoing mail is not configured on this instance, so no e-mail was sent. Give this link to{' '}
              <span className="font-semibold text-foreground">{inviteLink.email}</span> yourself — it works once and
              expires in 7 days.
            </p>
            <code
              aria-label="Invitation link"
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
                    toast.show('Invitation link copied');
                  } catch {
                    toast.show('Could not copy — select the link and copy it by hand', 'destructive');
                  }
                }}
              >
                <Copy size={14} /> Copy link
              </Button>
              <Button type="button" variant="primary" onClick={() => setInviteLink(null)}>
                Done
              </Button>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        open={!!roleTarget}
        onClose={() => setRoleTarget(null)}
        title={roleTarget ? `Change role · ${roleTarget.name}` : 'Change role'}
      >
        {roleTarget && (
          <div className="flex flex-col gap-2">
            {ROLES.map((r) => (
              <button
                key={r}
                type="button"
                onClick={async () => {
                  try {
                    await changeRole.mutateAsync({ userId: roleTarget.userId, role: r });
                    toast.show(`Role updated to ${r}`);
                    setRoleTarget(null);
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'failed to change role', 'destructive');
                  }
                }}
                className={
                  r === roleTarget.role
                    ? 'flex items-center gap-3 rounded-md border border-primary p-2.5'
                    : 'flex items-center gap-3 rounded-md border border-border p-2.5'
                }
              >
                <RoleBadge role={r} />
              </button>
            ))}
          </div>
        )}
      </Dialog>
    </>
  );
}
