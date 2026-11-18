import { Pill } from '../../ui/badge.tsx';
import { SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import type { BackupEntry } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { formatSize } from './backupFormat.ts';

/**
 * BackupStatusCard is the facts of Settings → Backups as rows: the last
 * backup that has an outcome, the next scheduled one, the destination and
 * the note on the master key.
 */
export function BackupStatusCard({
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
  // The newest entry that has an outcome; a running one has none yet.
  const last = items.find((i) => i.state !== 'running');

  return (
    <SettingsCard title={t.backups.strip.label} description={t.backups.strip.description}>
      <SettingsRow label={t.backups.strip.last}>
        {last ? (
          <span className="flex flex-wrap items-center gap-2 text-[13px]">
            <span>{f.dateTime(last.created_at)}</span>
            {last.size_bytes > 0 && <span className="text-muted-foreground">{formatSize(f, last.size_bytes)}</span>}
            <Pill tone={last.state === 'succeeded' ? 'ok' : last.state === 'abandoned' ? 'warn' : 'crit'}>
              {t.backups.list.states[last.state]}
            </Pill>
          </span>
        ) : (
          <span className="text-[13px] text-muted-foreground">{t.backups.strip.none}</span>
        )}
      </SettingsRow>
      <SettingsRow label={t.backups.strip.next}>
        <span className="text-[13px]">
          {nextRunAt ? f.dateTime(nextRunAt) : <span className="text-muted-foreground">{t.backups.strip.scheduleOff}</span>}
        </span>
      </SettingsRow>
      <SettingsRow label={t.backups.strip.destination}>
        <span className="text-[13px] break-words">{destinationLabel}</span>
      </SettingsRow>
      <SettingsRow label={t.backups.masterKeyLabel} help={t.backups.masterKey} />
    </SettingsCard>
  );
}
