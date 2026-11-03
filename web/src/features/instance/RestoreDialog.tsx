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

function CommandBlock({ label, hint, command }: { label: string; hint?: string; command: string }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[13px] font-semibold">{label}</span>
      <div className="flex items-start gap-2">
        <pre className="flex-1 overflow-x-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] whitespace-pre-wrap break-all">
          {command}
        </pre>
        <Button
          type="button"
          variant="ghost"
          aria-label={t.backups.restore.copy}
          onClick={() => void navigator.clipboard.writeText(command)}
        >
          <Copy size={13} />
        </Button>
      </div>
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </div>
  );
}
