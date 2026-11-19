import { Copy } from 'lucide-react';
import { copyText } from '../../lib/clipboard.ts';
import { useT } from '../../i18n/useT.ts';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { useToast } from '../../ui/toastContext.ts';

/** An invitation's acceptance link and the address it is for. */
export interface InviteLink {
  email: string;
  url: string;
}

/**
 * InviteLinkDialog hands an invitation's link to the administrator when the
 * instance has no outgoing mail configured, so no e-mail went out: on
 * creating an invitation (MembersPage) and on resending one
 * (PendingInvites). Closed while `link` is null.
 */
export function InviteLinkDialog({ link, onClose }: { link: InviteLink | null; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  return (
    <Dialog open={!!link} onClose={onClose} title={t.members.shareLink.title}>
      {link && (
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted-foreground">
            {t.members.shareLink.before} <span className="font-semibold text-foreground">{link.email}</span> {t.members.shareLink.after}
          </p>
          <code aria-label={t.members.shareLink.linkLabel} className="break-all rounded-md border border-border bg-background p-2 font-mono text-[11px]">
            {link.url}
          </code>
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              onClick={async () => {
                const ok = await copyText(link.url);
                if (ok) toast.show(t.members.shareLink.copied);
                else toast.show(t.members.shareLink.copyFailed, 'destructive');
              }}
            >
              <Copy size={14} /> {t.members.shareLink.copy}
            </Button>
            <Button type="button" variant="primary" onClick={onClose}>
              {t.members.shareLink.done}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}
