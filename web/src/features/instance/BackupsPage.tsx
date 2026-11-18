import { DatabaseBackup } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { SettingsPage } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useBackupSettings, useBackups, useCreateBackup } from '../../api/hooks/useBackups.ts';
import { useT } from '../../i18n/useT.ts';
import { BackupList } from './BackupList.tsx';
import { BackupSettingsForm } from './BackupSettingsForm.tsx';
import { BackupStatusCard } from './BackupStatusCard.tsx';

/**
 * BackupsPage is the instance's backup management, for instance
 * administrators (Settings → Backups): Back up now in the page header, a
 * status card, the Destination and Schedule cards (each saved on its own) and
 * the list of backups with Download, Delete and Restore…. Like mail and language it is filed under an
 * organization's Settings only for navigation; it is instance-wide.
 */
export function BackupsPage() {
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
    <SettingsPage
      title={t.backups.title}
      description={t.backups.hint}
      actions={<BackUpNowButton running={items.some((i) => i.state === 'running')} />}
    >
      {backups.data.storage_error && (
        <div
          role="alert"
          className="rounded-md border border-destructive/40 bg-card px-4 py-2.5 text-[13px] break-words text-destructive"
        >
          {t.backups.storageError(backups.data.storage_error)}
        </div>
      )}

      <BackupStatusCard items={items} nextRunAt={backups.data.next_run_at} destinationLabel={backups.data.destination_label} />

      <BackupSettingsForm settings={settings.data} />

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
    </SettingsPage>
  );
}

function BackUpNowButton({ running }: { running: boolean }) {
  const t = useT();
  const toast = useToast();
  const create = useCreateBackup();
  return (
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
  );
}
