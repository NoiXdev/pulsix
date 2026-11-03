import { useRef, useState } from 'react';
import { Copy } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';
import { restoreCommands, type RestoreDestination } from './restoreCommands.ts';

/**
 * RestoreDialog shows the command lines that restore one backup. Restoring
 * replaces the whole database and needs the manager stopped, so it is never
 * a button in the running instance: this is also what is needed when the
 * instance is broken or new. The commands are the ones in
 * deploy/compose/README.md.
 */
export function RestoreDialog({
  name,
  destination,
  onClose,
}: {
  name: string;
  destination: RestoreDestination;
  onClose: () => void;
}) {
  const t = useT();
  const commands = restoreCommands(name, destination);
  return (
    <Dialog open onClose={onClose} title={t.backups.restore.title} size="wide">
      <div className="flex flex-col gap-4">
        <p className="text-xs text-muted-foreground">{t.backups.restore.intro}</p>
        {destination.destination === 's3' && (
          <p className="text-xs text-muted-foreground">{t.backups.restore.s3Credentials}</p>
        )}
        <CommandBlock label={t.backups.restore.byName} command={commands.byName} />
        <CommandBlock label={t.backups.restore.fromFile} hint={t.backups.restore.fromFileHint} command={commands.fromFile} />
        <p className="text-xs text-muted-foreground">{t.backups.restore.note}</p>
        <div className="flex justify-end">
          <Button type="button" variant="primary" onClick={onClose}>
            {t.backups.restore.done}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

/**
 * One command with a copy button. `navigator.clipboard` exists only in a
 * secure context, and a compose install is often reached over plain
 * http://host:port, so the button cannot assume it: where the Clipboard API
 * is missing or refuses, the command text is selected so Ctrl/Cmd+C works
 * and the page says so. It never throws.
 */
function CommandBlock({ label, hint, command }: { label: string; hint?: string; command: string }) {
  const t = useT();
  const pre = useRef<HTMLPreElement>(null);
  const [feedback, setFeedback] = useState<'copied' | 'manual' | null>(null);

  function selectText() {
    const el = pre.current;
    const selection = window.getSelection();
    if (!el || !selection) return;
    const range = document.createRange();
    range.selectNodeContents(el);
    selection.removeAllRanges();
    selection.addRange(range);
  }

  async function copy() {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(command);
      setFeedback('copied');
    } catch {
      selectText();
      setFeedback('manual');
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[13px] font-semibold">{label}</span>
      <div className="flex items-start gap-2">
        <pre
          ref={pre}
          className="flex-1 overflow-x-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] whitespace-pre-wrap break-all"
        >
          {command}
        </pre>
        <Button type="button" variant="ghost" aria-label={t.backups.restore.copy} onClick={() => void copy()}>
          <Copy size={13} />
        </Button>
      </div>
      {feedback && (
        <span role="status" className="text-[11px] text-muted-foreground">
          {feedback === 'copied' ? t.backups.restore.copied : t.backups.restore.copyManual}
        </span>
      )}
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </div>
  );
}
