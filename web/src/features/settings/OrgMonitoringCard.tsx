import { useEffect, useState } from 'react';
import { useMonitoringSettings, useUpdateMonitoringSettings, type RetentionChoice } from '../../api/hooks/useMonitoringSettings.ts';
import { Input } from '../../ui/input.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { Select, type SelectOption } from '../../ui/Select.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
import { useT } from '../../i18n/useT.ts';
import { ReadOnlyNote } from './ReadOnlyNote.tsx';

/** The range the server accepts (internal/monitorsettings: 0..365). */
const MAX_DAYS = 365;

/** The retention choices the server accepts (internal/monitorsettings). */
const RETENTION_CHOICES = [30, 90, 365] as const;
const DEFAULT_CHOICE = 'default';

/** choiceOf maps the select's value back to what the API takes. */
function choiceOf(value: string): RetentionChoice {
  return value === DEFAULT_CHOICE ? null : (Number(value) as RetentionChoice);
}

/** parseDays reads the field as a whole number of days in 0..365, or null. */
function parseDays(text: string): number | null {
  if (!/^\d+$/.test(text.trim())) return null;
  const n = Number(text.trim());
  return n <= MAX_DAYS ? n : null;
}

/**
 * OrgMonitoringCard is the organization's monitoring settings on Settings →
 * General (backlog 3.57): after how many days without contact a client
 * host's absent warning opens, 0 switching it off, and how long
 * measurements are kept. Every member reads it; only a role with
 * org.settings.write (admins and owners) may change it, the same gate the
 * server applies. A form card: Save stays disabled until something changes.
 */
export function OrgMonitoringCard({
  org,
  canWrite,
  onDirtyChange,
}: {
  org: string;
  canWrite: boolean;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const t = useT();
  const m = t.settings.monitoring;
  const toast = useToast();
  const confirm = useConfirm();
  const settings = useMonitoringSettings(org);
  const update = useUpdateMonitoringSettings(org);
  const [invalid, setInvalid] = useState(false);

  const stored = settings.data?.settings.client_absent_days;
  const maxDays = settings.data?.settings.retention_max_days;
  const storedChoice = settings.data?.settings.retention_days ?? null;
  const saved = {
    days: stored === undefined ? '' : String(stored),
    retention: storedChoice === null ? DEFAULT_CHOICE : String(storedChoice),
  };
  const form = useCardForm(saved, { guard: false });
  const { draft, dirty } = form;
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  // A card that goes away (an error state) leaves nothing unsaved behind.
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  const retentionOptions: SelectOption[] =
    maxDays === undefined
      ? []
      : [
          { value: DEFAULT_CHOICE, label: m.retentionDefault(maxDays) },
          ...RETENTION_CHOICES.map((days) => ({
            value: String(days),
            label: m.retentionDays(days),
            disabled: days > maxDays,
            hint: days > maxDays ? m.retentionAboveMax(maxDays) : undefined,
          })),
        ];

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canWrite) return;
    const days = parseDays(draft.days);
    if (days === null) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    // The retention is sent only when the select was changed: left out, the
    // server keeps the stored choice, which another admin may have changed
    // since this page loaded.
    const retentionDays = draft.retention === saved.retention ? undefined : choiceOf(draft.retention);
    // Lowering what is kept deletes data, so it is asked first.
    const current = settings.data?.settings.retention_effective_days;
    const next = maxDays === undefined || retentionDays === undefined ? undefined : Math.min(retentionDays ?? maxDays, maxDays);
    if (current !== undefined && next !== undefined && next < current) {
      const ok = await confirm({ title: m.retentionLowerTitle, message: m.retentionLowerWarning(next), destructive: true });
      if (!ok) return;
    }
    try {
      await update.mutateAsync({ clientAbsentDays: days, retentionDays });
      form.markSaved({ days: String(days), retention: draft.retention }, draft);
      toast.show(m.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.saveFailed, 'destructive');
    }
  }

  return (
    <form id="org-monitoring-form" noValidate onSubmit={(e) => void onSubmit(e)}>
      <SettingsCard
        title={m.title}
        description={m.description}
        footer={
          settings.isError ? null : canWrite ? (
            <CardSaveBar
              formId="org-monitoring-form"
              dirty={dirty}
              saving={settings.isPending || update.isPending}
              onDiscard={() => {
                form.discard();
                setInvalid(false);
              }}
              saveLabel={m.saveLabel}
            />
          ) : (
            <ReadOnlyNote>{m.readOnlyHint}</ReadOnlyNote>
          )
        }
      >
        {settings.isError ? (
          <p role="alert" className="border-t border-border px-5 py-3.5 text-xs text-destructive">
            {m.loadFailed}
          </p>
        ) : (
          <SettingsRow label={m.clientAbsentDays} htmlFor="client_absent_days" help={m.clientAbsentDaysHint} helpId="client_absent_days_help">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                id="client_absent_days"
                type="number"
                inputMode="numeric"
                min={0}
                max={MAX_DAYS}
                step={1}
                className="w-24"
                value={draft.days}
                readOnly={!canWrite}
                disabled={settings.isPending}
                aria-invalid={invalid || undefined}
                aria-describedby={invalid ? 'client_absent_days_help client_absent_days_error' : 'client_absent_days_help'}
                onChange={(e) => {
                  form.setDraft((d) => ({ ...d, days: e.target.value }));
                  setInvalid(false);
                }}
              />
              <span className="text-[12.5px] text-muted-foreground">{m.days}</span>
              {invalid && (
                <span id="client_absent_days_error" role="alert" className="basis-full text-[11px] text-destructive">
                  {m.invalid}
                </span>
              )}
            </div>
          </SettingsRow>
        )}
        {!settings.isError && maxDays !== undefined && (
          <SettingsRow label={m.retention} htmlFor="retention_days" help={m.retentionHint} helpId="retention_days_help">
            <div className="w-60 max-w-full">
              <Select
                id="retention_days"
                aria-describedby="retention_days_help"
                portal
                options={retentionOptions}
                value={draft.retention}
                onChange={(v) => form.setDraft((d) => ({ ...d, retention: v }))}
                disabled={!canWrite}
              />
            </div>
          </SettingsRow>
        )}
      </SettingsCard>
    </form>
  );
}
