import { useState } from 'react';
import { ApiError } from '../../api/client.ts';
import { useResendInvite } from '../../api/hooks/useMembers.ts';
import type { PendingInvite } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import type { InviteLink } from './InviteLinkDialog.tsx';

/**
 * useResendInviteCommand sends an invitation again (backlog 3.125): a toast
 * when the e-mail went out, the new link to hand over (`link`, for
 * InviteLinkDialog) when the instance has no outgoing mail, and on the
 * server's own 502 — the mail could not be delivered, so the server left
 * the invitation as it was — a toast that says so. A 502 without the
 * server's problem body (a proxy's) proves nothing about the invitation
 * and gets the generic error toast. `pending` holds the invitations whose
 * request runs.
 */
export function useResendInviteCommand(org: string) {
  const t = useT();
  const p = t.members.pending;
  const toast = useToast();
  const resend = useResendInvite(org);
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const [link, setLink] = useState<InviteLink | null>(null);

  async function run(invite: PendingInvite) {
    if (pending.has(invite.id)) return;
    setPending((s) => new Set(s).add(invite.id));
    try {
      const res = await resend.mutateAsync(invite.id);
      if (res.mail_sent || !res.accept_url) toast.show(p.resent(res.email));
      else setLink({ email: res.email, url: res.accept_url });
    } catch (err) {
      if (err instanceof ApiError && !isServerProblem(err.body)) toast.show(p.resendFailed, 'destructive');
      else if (err instanceof ApiError && err.status === 502) toast.show(p.resendMailFailed, 'destructive');
      else toast.show(err instanceof Error ? err.message : p.resendFailed, 'destructive');
    } finally {
      setPending((s) => {
        const next = new Set(s);
        next.delete(invite.id);
        return next;
      });
    }
  }

  return { run, pending, link, closeLink: () => setLink(null) };
}

/** Whether an error body is the server's problem document (it carries a
 * `detail` or a `type`), not a proxy's page. */
function isServerProblem(body: unknown): boolean {
  if (!body || typeof body !== 'object') return false;
  return ('detail' in body && typeof body.detail === 'string') || ('type' in body && typeof body.type === 'string');
}
