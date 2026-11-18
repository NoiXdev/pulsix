import { useEffect, useState } from 'react';
import { Input } from '../../ui/input.tsx';
import { Switch } from '../../ui/switch.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
import type { BackupSettings, BackupSettingsInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { toBody, toDestinationDraft, toScheduleDraft, type ScheduleDraft } from './backupDraft.ts';

const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
const COUNT_RE = /^\d+$/;

/**
 * BackupScheduleCard is the Schedule and retention card of Settings →
 * Backups. Saving sends this draft on top of the last saved destination,
 * never the destination card's unsaved draft; the stored S3 secret is left
 * alone because the request carries no `secret_key`. The save is shared
 * with the destination card (`onSave`, `saving`).
 */
export function BackupScheduleCard({
  settings,
  onSave,
  saving,
  onDirtyChange,
}: {
  settings: BackupSettings;
  onSave: (body: BackupSettingsInput) => Promise<void>;
  saving: boolean;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const t = useT();
  const sc = t.backups.schedule;
  const toast = useToast();
  const form = useCardForm(toScheduleDraft(settings, true), { guard: false });
  const { draft, dirty } = form;
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  const [errors, setErrors] = useState<{ time?: string; retention?: string }>({});
  const set = <K extends keyof ScheduleDraft>(key: K, value: ScheduleDraft[K]) =>
    form.setDraft((cur) => ({ ...cur, [key]: value }));

  function validate(): boolean {
    const next: typeof errors = {};
    if (!TIME_RE.test(draft.timeOfDay.trim())) next.time = sc.invalidTime;
    if (![draft.daily, draft.weekly, draft.monthly].every((v) => COUNT_RE.test(v.trim()))) {
      next.retention = sc.invalidRetention;
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!dirty || !validate()) return;
    try {
      await onSave(toBody(toDestinationDraft(settings), draft));
      form.markSaved(draft);
      toast.show(t.backups.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.backups.saveFailed, 'destructive');
    }
  }

  const count = (id: string, label: string, key: 'daily' | 'weekly', help?: string) => (
    <SettingsRow label={label} htmlFor={id} help={help}>
      <Input
        id={id}
        type="number"
        min={0}
        className="w-28"
        value={draft[key]}
        onChange={(e) => set(key, e.target.value)}
      />
    </SettingsRow>
  );

  return (
    <form id="backup-schedule-form" onSubmit={(e) => void onSubmit(e)}>
      <SettingsCard
        title={sc.title}
        description={sc.description}
        footer={
          <CardSaveBar
            formId="backup-schedule-form"
            dirty={dirty}
            saving={saving}
            onDiscard={() => {
              form.discard();
              setErrors({});
            }}
            saveLabel={sc.save}
          />
        }
      >
        <SettingsRow label={sc.enabled} htmlFor="backup_enabled">
          <Switch id="backup_enabled" checked={draft.enabled} onCheckedChange={(c) => set('enabled', c)} />
        </SettingsRow>
        <SettingsRow label={sc.timeOfDay} htmlFor="backup_time">
          <div className="flex flex-col gap-1.5">
            <Input
              id="backup_time"
              className="w-28"
              placeholder="03:00"
              value={draft.timeOfDay}
              onChange={(e) => set('timeOfDay', e.target.value)}
            />
            {errors.time && <span className="text-[12.5px] text-destructive">{errors.time}</span>}
          </div>
        </SettingsRow>
        <SettingsRow label={sc.timeZone} htmlFor="backup_zone">
          <Input
            id="backup_zone"
            className="w-60"
            placeholder="Europe/Berlin"
            value={draft.timeZone}
            onChange={(e) => set('timeZone', e.target.value)}
          />
        </SettingsRow>
        {count('backup_daily', sc.daily, 'daily', sc.keep)}
        {count('backup_weekly', sc.weekly, 'weekly')}
        <SettingsRow label={sc.monthly} htmlFor="backup_monthly" help={sc.keepHint}>
          <div className="flex flex-col gap-1.5">
            <Input
              id="backup_monthly"
              type="number"
              min={0}
              className="w-28"
              value={draft.monthly}
              onChange={(e) => set('monthly', e.target.value)}
            />
            {errors.retention && <span className="text-[12.5px] text-destructive">{errors.retention}</span>}
          </div>
        </SettingsRow>
        <SettingsRow label={sc.includeMetrics} htmlFor="backup_include_metrics">
          <Switch
            id="backup_include_metrics"
            checked={draft.includeMetrics}
            onCheckedChange={(c) => set('includeMetrics', c)}
          />
        </SettingsRow>
      </SettingsCard>
    </form>
  );
}
