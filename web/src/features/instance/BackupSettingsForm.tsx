import { useState, type ReactNode } from 'react';
import { PlugZap, Save } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useSaveBackupSettings, useTestBackupSettings } from '../../api/hooks/useBackups.ts';
import type { BackupSettings, BackupSettingsInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/** Where the folder destination lives inside the manager container. */
const FOLDER_PATH = '/var/lib/pulsix/backups';

const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
const COUNT_RE = /^\d+$/;

interface Draft {
  destination: 'folder' | 's3';
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  accessKey: string;
  /** What was typed into the secret field; empty means "leave it alone". */
  secretKey: string;
  removeSecret: boolean;
  pathStyle: boolean;
  enabled: boolean;
  timeOfDay: string;
  timeZone: string;
  daily: string;
  weekly: string;
  monthly: string;
  includeMetrics: boolean;
}

/** The browser's own zone, offered while nothing has been saved. */
function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

function toDraft(s: BackupSettings): Draft {
  return {
    destination: s.destination,
    endpoint: s.s3.endpoint,
    region: s.s3.region,
    bucket: s.s3.bucket,
    prefix: s.s3.prefix,
    accessKey: s.s3.access_key,
    secretKey: '',
    removeSecret: false,
    pathStyle: s.s3.path_style,
    enabled: s.enabled,
    timeOfDay: s.time_of_day,
    // The API answers UTC for an instance that has never saved a schedule;
    // an administrator who has not chosen a zone yet most likely means
    // their own, so that is what the field starts with.
    timeZone: !s.enabled && s.time_zone === 'UTC' ? browserZone() : s.time_zone,
    daily: String(s.retention.daily),
    weekly: String(s.retention.weekly),
    monthly: String(s.retention.monthly),
    includeMetrics: s.include_metrics,
  };
}

/**
 * The request body for a draft. `secret_key` is write-only: absent keeps the
 * stored one (the normal case, since the page cannot show it back), a typed
 * value replaces it, and only the explicit checkbox sends "" to remove it.
 */
function toBody(d: Draft): BackupSettingsInput {
  const s3: NonNullable<BackupSettingsInput['s3']> = {
    endpoint: d.endpoint.trim(),
    region: d.region.trim(),
    bucket: d.bucket.trim(),
    prefix: d.prefix.trim(),
    access_key: d.accessKey.trim(),
    path_style: d.pathStyle,
  };
  if (d.secretKey) s3.secret_key = d.secretKey;
  else if (d.removeSecret) s3.secret_key = '';
  return {
    destination: d.destination,
    s3,
    enabled: d.enabled,
    time_of_day: d.timeOfDay.trim(),
    time_zone: d.timeZone.trim(),
    retention: { daily: Number(d.daily), weekly: Number(d.weekly), monthly: Number(d.monthly) },
    include_metrics: d.includeMetrics,
  };
}

type TestResult = { ok: boolean; text: string } | null;

/**
 * BackupSettingsForm is the destination and the schedule-and-retention
 * cards: one PUT carries both, so they share one draft and one Save button.
 * Test connection probes the destination as drafted, without saving.
 */
export function BackupSettingsForm({ settings }: { settings: BackupSettings }) {
  const t = useT();
  const toast = useToast();
  const save = useSaveBackupSettings();
  const test = useTestBackupSettings();
  const [draft, setDraft] = useState(() => toDraft(settings));
  const [errors, setErrors] = useState<{ time?: string; retention?: string }>({});
  const [testResult, setTestResult] = useState<TestResult>(null);
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const s3 = draft.destination === 's3';

  function validate(): boolean {
    const next: typeof errors = {};
    if (!TIME_RE.test(draft.timeOfDay.trim())) next.time = t.backups.schedule.invalidTime;
    if (![draft.daily, draft.weekly, draft.monthly].every((v) => COUNT_RE.test(v.trim()))) {
      next.retention = t.backups.schedule.invalidRetention;
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!validate()) return;
        try {
          await save.mutateAsync(toBody(draft));
          save.reset();
          // Whatever was typed into the secret field has been sent; the
          // stored secret is now the source of truth again.
          setDraft((d) => ({ ...d, secretKey: '', removeSecret: false }));
          toast.show(t.backups.saved);
        } catch (err) {
          toast.show(err instanceof Error ? err.message : t.backups.saveFailed, 'destructive');
        }
      }}
    >
      <Card>
        <div className="flex flex-col gap-3 p-4">
          <h2 className="text-sm font-semibold">{t.backups.destination.title}</h2>
          <div className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
            <label className="flex items-center gap-2">
              <input
                type="radio"
                name="backup_destination"
                checked={!s3}
                onChange={() => set('destination', 'folder')}
              />
              {t.backups.destination.folder} (<code>{FOLDER_PATH}</code>)
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" name="backup_destination" checked={s3} onChange={() => set('destination', 's3')} />
              {t.backups.destination.s3}
            </label>
          </div>
          {s3 && (
            <div className="grid grid-cols-2 gap-3">
              <Field id="backup_endpoint" label={t.backups.destination.endpoint} hint={t.backups.destination.endpointHint}>
                <Input
                  id="backup_endpoint"
                  placeholder="https://s3.eu-central-1.example.com"
                  value={draft.endpoint}
                  onChange={(e) => set('endpoint', e.target.value)}
                />
              </Field>
              <Field id="backup_region" label={t.backups.destination.region}>
                <Input
                  id="backup_region"
                  placeholder="eu-central-1"
                  value={draft.region}
                  onChange={(e) => set('region', e.target.value)}
                />
              </Field>
              <Field id="backup_bucket" label={t.backups.destination.bucket}>
                <Input id="backup_bucket" value={draft.bucket} onChange={(e) => set('bucket', e.target.value)} />
              </Field>
              <Field id="backup_prefix" label={t.backups.destination.prefix}>
                <Input
                  id="backup_prefix"
                  placeholder="pulsix/"
                  value={draft.prefix}
                  onChange={(e) => set('prefix', e.target.value)}
                />
              </Field>
              <Field id="backup_access_key" label={t.backups.destination.accessKey}>
                <Input
                  id="backup_access_key"
                  autoComplete="off"
                  value={draft.accessKey}
                  onChange={(e) => set('accessKey', e.target.value)}
                />
              </Field>
              <Field
                id="backup_secret_key"
                label={t.backups.destination.secretKey}
                hint={settings.s3.secret_key_set ? t.backups.destination.secretStored : t.backups.destination.secretNone}
              >
                <Input
                  id="backup_secret_key"
                  type="password"
                  autoComplete="new-password"
                  value={draft.secretKey}
                  onChange={(e) => set('secretKey', e.target.value)}
                />
              </Field>
              <div className="col-span-2 flex items-center gap-2 text-xs">
                <Switch id="backup_path_style" checked={draft.pathStyle} onCheckedChange={(c) => set('pathStyle', c)} />
                <label htmlFor="backup_path_style">{t.backups.destination.pathStyle}</label>
              </div>
              {settings.s3.secret_key_set && (
                <div className="col-span-2 flex items-center gap-2 text-[11px] text-muted-foreground">
                  <Switch
                    id="backup_remove_secret"
                    checked={draft.removeSecret}
                    onCheckedChange={(c) => set('removeSecret', c)}
                  />
                  <label htmlFor="backup_remove_secret">{t.backups.destination.removeSecret}</label>
                </div>
              )}
            </div>
          )}
          <div className="flex flex-col items-start gap-2">
            <Button
              type="button"
              disabled={test.isPending}
              onClick={async () => {
                setTestResult(null);
                try {
                  await test.mutateAsync(toBody(draft));
                  setTestResult({ ok: true, text: t.backups.destination.testOk });
                } catch (err) {
                  setTestResult({
                    ok: false,
                    text: err instanceof Error ? err.message : t.backups.destination.testFailed,
                  });
                }
              }}
            >
              <PlugZap size={14} /> {t.backups.destination.test}
            </Button>
            {testResult && (
              <p
                role={testResult.ok ? 'status' : 'alert'}
                className={`text-xs break-words ${testResult.ok ? 'text-muted-foreground' : 'text-destructive'}`}
              >
                {testResult.text}
              </p>
            )}
          </div>
        </div>
      </Card>

      <Card>
        <div className="flex flex-col gap-3 p-4">
          <h2 className="text-sm font-semibold">{t.backups.schedule.title}</h2>
          <div className="flex items-center gap-2 text-xs">
            <Switch id="backup_enabled" checked={draft.enabled} onCheckedChange={(c) => set('enabled', c)} />
            <label htmlFor="backup_enabled">{t.backups.schedule.enabled}</label>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field id="backup_time" label={t.backups.schedule.timeOfDay} error={errors.time}>
              <Input
                id="backup_time"
                placeholder="03:00"
                value={draft.timeOfDay}
                onChange={(e) => set('timeOfDay', e.target.value)}
              />
            </Field>
            <Field id="backup_zone" label={t.backups.schedule.timeZone}>
              <Input
                id="backup_zone"
                placeholder="Europe/Berlin"
                value={draft.timeZone}
                onChange={(e) => set('timeZone', e.target.value)}
              />
            </Field>
          </div>
          <span className="text-xs font-medium">{t.backups.schedule.keep}</span>
          <div className="grid grid-cols-3 gap-3">
            <Field id="backup_daily" label={t.backups.schedule.daily}>
              <Input
                id="backup_daily"
                type="number"
                min={0}
                value={draft.daily}
                onChange={(e) => set('daily', e.target.value)}
              />
            </Field>
            <Field id="backup_weekly" label={t.backups.schedule.weekly}>
              <Input
                id="backup_weekly"
                type="number"
                min={0}
                value={draft.weekly}
                onChange={(e) => set('weekly', e.target.value)}
              />
            </Field>
            <Field id="backup_monthly" label={t.backups.schedule.monthly}>
              <Input
                id="backup_monthly"
                type="number"
                min={0}
                value={draft.monthly}
                onChange={(e) => set('monthly', e.target.value)}
              />
            </Field>
          </div>
          {errors.retention && <span className="text-[11px] text-destructive">{errors.retention}</span>}
          <span className="text-[11px] text-muted-foreground">{t.backups.schedule.keepHint}</span>
          <div className="flex items-center gap-2 text-xs">
            <Switch
              id="backup_include_metrics"
              checked={draft.includeMetrics}
              onCheckedChange={(c) => set('includeMetrics', c)}
            />
            <label htmlFor="backup_include_metrics">{t.backups.schedule.includeMetrics}</label>
          </div>
          <p className="text-[11px] text-muted-foreground">{t.backups.masterKey}</p>
        </div>
      </Card>

      <div className="flex justify-end">
        <Button type="submit" variant="primary" disabled={save.isPending}>
          <Save size={14} /> {t.backups.save}
        </Button>
      </div>
    </form>
  );
}

function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  );
}
