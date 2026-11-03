import { useParams } from '@tanstack/react-router';
import { DatabaseBackup } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
import { useBackupSettings, useBackups, useCreateBackup } from '../../api/hooks/useBackups.ts';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { formatSize } from './backupFormat.ts';
import { BackupList } from './BackupList.tsx';
import { BackupSettingsForm } from './BackupSettingsForm.tsx';

/**
 * BackupsPage is the instance's backup management, for instance
 * administrators (Settings → Backups): a status strip with Back up now, the
 * destination and schedule form, and the list of backups with Download,
 * Delete and Restore…. Like mail and language it is filed under an
 * organization's Settings only for navigation; it is instance-wide.
 */
export function BackupsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const settings = useBackupSettings();
  const backups = useBackups();

  const settingsGuard = useQueryGuard(settings);
  const listGuard = useQueryGuard(backups);
  if (settingsGuard) return settingsGuard;
  if (listGuard) return listGuard;
  if (!settings.data || !backups.data) return <QueryLoading />;

  const items = backups.data.items ?? [];
  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">{t.backups.title}</h1>
        <SettingsTabs org={org} />
      </div>
      <span className="text-[13px] text-muted-foreground">{t.backups.hint}</span>

      {backups.data.storage_error && (
        <div
          role="alert"
          className="rounded-md border border-destructive/40 bg-card px-4 py-2.5 text-[13px] break-words text-destructive"
        >
          {t.backups.storageError(backups.data.storage_error)}
        </div>
      )}

      <StatusStrip items={items} nextRunAt={backups.data.next_run_at} destinationLabel={backups.data.destination_label} />

      <div className="grid grid-cols-1 items-start gap-4">
        <BackupSettingsForm key={JSON.stringify(settings.data)} settings={settings.data} />
      </div>

      <h2 className="text-sm font-semibold">{t.backups.list.title}</h2>
      <BackupList
        items={items}
        destination={{
          destination: settings.data.destination,
          bucket: settings.data.s3.bucket,
          prefix: settings.data.s3.prefix,
          endpoint: settings.data.s3.endpoint,
          region: settings.data.s3.region,
          pathStyle: settings.data.s3.path_style,
        }}
      />
    </>
  );
}

function StatusStrip({
  items,
  nextRunAt,
  destinationLabel,
}: {
  items: BackupEntry[];
  nextRunAt: string | null;
  destinationLabel: string;
}) {
  const t = useT();
  const f = useFormat();
  const toast = useToast();
  const create = useCreateBackup();
  // The newest entry that has an outcome; a running one has none yet.
  const last = items.find((i) => i.state !== 'running');
  const running = items.some((i) => i.state === 'running');

  return (
    <section aria-label={t.backups.strip.label} className="flex flex-wrap items-stretch gap-3">
      <Stat label={t.backups.strip.last}>
        {last ? (
          <span className="flex flex-wrap items-center gap-2">
            <span>{f.dateTime(last.created_at)}</span>
            {last.size_bytes > 0 && <span className="text-muted-foreground">{formatSize(f, last.size_bytes)}</span>}
            <Pill tone={last.state === 'succeeded' ? 'ok' : last.state === 'abandoned' ? 'warn' : 'crit'}>
              {t.backups.list.states[last.state]}
            </Pill>
          </span>
        ) : (
          <span className="text-muted-foreground">{t.backups.strip.none}</span>
        )}
      </Stat>
      <Stat label={t.backups.strip.next}>
        {nextRunAt ? f.dateTime(nextRunAt) : <span className="text-muted-foreground">{t.backups.strip.scheduleOff}</span>}
      </Stat>
      <Stat label={t.backups.strip.destination}>{destinationLabel}</Stat>
      <div className="flex items-center">
        <Button
          type="button"
          variant="primary"
          disabled={running || create.isPending}
          onClick={async () => {
            try {
              await create.mutateAsync();
              toast.show(t.backups.strip.started);
            } catch (err) {
              toast.show(err instanceof Error ? err.message : t.backups.strip.startFailed, 'destructive');
            }
          }}
        >
          <DatabaseBackup size={14} /> {t.backups.strip.backUpNow}
        </Button>
      </div>
    </section>
  );
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-[180px] flex-1 flex-col gap-1 rounded-[10px] border border-border bg-card px-4 py-3">
      <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">{label}</span>
      <span className="text-[13px] break-words">{children}</span>
    </div>
  );
}
