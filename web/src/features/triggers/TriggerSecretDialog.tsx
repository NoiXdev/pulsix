import { Copy } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { useT } from '../../i18n/useT.ts';
import { SecretBox } from './SecretBox.tsx';

/** TriggerSecretDialog shows the secret an approved trigger request drew
 * (spec 2026-11-25 C), once, to the person who approved it: the URL and the
 * secret with copy, like the trigger dialog's own secret box. Closing it
 * forgets the secret; nothing can show it again. */
export function TriggerSecretDialog({ secret, url, onClose }: { secret: string; url: string; onClose: () => void }) {
  const t = useT().triggers;
  const copy = useCopy();
  return (
    <Dialog open onClose={onClose} title={t.request.secretTitle}>
      <div className="flex flex-col gap-3">
        <p className="text-xs text-muted-foreground">{t.request.secretHint}</p>
        {url && (
          <div className="flex items-center gap-2">
            <span className="text-xs font-semibold">POST</span>
            <code className="flex-1 truncate font-mono text-xs">{url}</code>
            <Button type="button" variant="ghost" aria-label={t.copyUrl} onClick={() => void copy(url)}>
              <Copy size={13} />
            </Button>
          </div>
        )}
        <SecretBox secret={secret} onDone={onClose} />
      </div>
    </Dialog>
  );
}
