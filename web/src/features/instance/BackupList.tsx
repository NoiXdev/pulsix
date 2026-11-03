import { useState } from 'react';
import { Download, RotateCcw, Trash2 } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { backupDownloadUrl, useDeleteBackup } from '../../api/hooks/useBackups.ts';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { formatSize } from './backupFormat.ts';
import { RestoreDialog } from './RestoreDialog.tsx';
import type { RestoreDestination } from './restoreCommands.ts';

const COLUMNS = '1.3fr 1.2fr 0.6fr 0.7fr 2fr 1.9fr';

const STATE_TONE: Record<BackupEntry['state'], Tone> = {
  running: 'accent',
  succeeded: 'ok',
  failed: 'crit',
  abandoned: 'warn',
  damaged: 'crit',
};

/**
 * BackupList is the table of backups at the destination merged with recent
 * attempts. A stored file (succeeded, or damaged — its header cannot be
 * read) can be downloaded and deleted; only a readable one can be restored.
 * An attempt that left no file (running, failed, interrupted) has no
 * actions: there is nothing to act on.
 */
export function BackupList({ items, destination }: { items: BackupEntry[]; destination: RestoreDestination }) {
  const t = useT();
  const f = useFormat();
  const toast = useToast();
  const del = useDeleteBackup();
  const [restoring, setRestoring] = useState<string | null>(null);

  return (
    <>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.backups.list.time}</span>
          <span>{t.backups.list.kind}</span>
          <span>{t.backups.list.version}</span>
          <span>{t.backups.list.size}</span>
          <span>{t.backups.list.state}</span>
          <span />
        </GridHeader>
        {items.length === 0 && <div className="px-4 py-6 text-[13px] text-muted-foreground">{t.backups.list.empty}</div>}
        {items.map((item) => {
          const stored = item.state === 'succeeded' || item.state === 'damaged';
          return (
            <GridRow key={`${item.name}|${item.created_at}`} columns={COLUMNS} className="text-[13px]">
              <span>{f.dateTime(item.created_at)}</span>
              <span>{item.kind === 'manual' ? t.backups.list.manual(item.actor_name) : t.backups.list.scheduled}</span>
              <span>{item.pulsix_version || '—'}</span>
              <span>{item.size_bytes > 0 ? formatSize(f, item.size_bytes) : '—'}</span>
              <span className="flex flex-col items-start gap-1">
                <Pill tone={STATE_TONE[item.state]}>{t.backups.list.states[item.state]}</Pill>
                {item.error && <span className="text-[11px] break-words text-destructive">{item.error}</span>}
              </span>
              <span className="flex flex-wrap justify-end gap-2">
                {stored && (
                  <a
                    href={backupDownloadUrl(item.name)}
                    download={item.name}
                    className="inline-flex h-[34px] items-center justify-center gap-2 rounded-md border border-border bg-card px-3.5 text-[13px] font-semibold text-foreground transition-colors hover:bg-accent"
                  >
                    <Download size={14} /> {t.backups.list.download}
                  </a>
                )}
                {item.state === 'succeeded' && (
                  <Button type="button" onClick={() => setRestoring(item.name)}>
                    <RotateCcw size={14} /> {t.backups.list.restore}
                  </Button>
                )}
                {stored && (
                  <Button
                    type="button"
                    variant="destructive"
                    disabled={del.isPending}
                    onClick={async () => {
                      if (!confirm(t.backups.list.deleteConfirm(item.name))) return;
                      try {
                        await del.mutateAsync(item.name);
                        toast.show(t.backups.list.deleted);
                      } catch (err) {
                        toast.show(err instanceof Error ? err.message : t.backups.list.deleteFailed, 'destructive');
                      }
                    }}
                  >
                    <Trash2 size={14} /> {t.backups.list.delete}
                  </Button>
                )}
              </span>
            </GridRow>
          );
        })}
      </Card>
      {restoring && <RestoreDialog name={restoring} destination={destination} onClose={() => setRestoring(null)} />}
    </>
  );
}
