import { useState } from 'react';
import { useMonitoringSettings, useUpdateMonitoringSettings, type RetentionChoice } from '../../api/hooks/useMonitoringSettings.ts';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select, type SelectOption } from '../../ui/Select.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { Card } from '../../ui/table.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';

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
 * host's absent warning opens, 0 switching it off. Every member reads it;
 * only a role with org.settings.write (admins and owners) may change it,
 * the same gate the server applies.
 */
export function OrgMonitoringCard({ org, canWrite }: { org: string; canWrite: boolean }) {
  const t = useT();
  const m = t.settings.monitoring;
  const toast = useToast();
  const confirm = useConfirm();
  const settings = useMonitoringSettings(org);
  const update = useUpdateMonitoringSettings(org);
  const [draft, setDraft] = useState<string | null>(null);
  const [invalid, setInvalid] = useState(false);
  const [retentionDraft, setRetentionDraft] = useState<string | null>(null);

  const stored = settings.data?.settings.client_absent_days;
  const text = draft ?? (stored === undefined ? '' : String(stored));

  const maxDays = settings.data?.settings.retention_max_days;
  const storedChoice = settings.data?.settings.retention_days ?? null;
  const retentionValue = retentionDraft ?? (storedChoice === null ? DEFAULT_CHOICE : String(storedChoice));
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
  const dirty = draft !== null || retentionDraft !== null;

  return (
    <Card>
      <form
        noValidate
        className="flex flex-col gap-3 p-4"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!canWrite) return;
          const days = parseDays(text);
          if (days === null) {
            setInvalid(true);
            return;
          }
          setInvalid(false);
          // The retention is sent only when the select was changed: left
          // out, the server keeps the stored choice, which another admin may
          // have changed since this page loaded.
          const retentionDays = retentionDraft === null ? undefined : choiceOf(retentionDraft);
          // Lowering what is kept deletes data, so it is asked first.
          const current = settings.data?.settings.retention_effective_days;
          const next =
            maxDays === undefined || retentionDays === undefined ? undefined : Math.min(retentionDays ?? maxDays, maxDays);
          if (current !== undefined && next !== undefined && next < current) {
            const ok = await confirm({ title: m.retentionLowerTitle, message: m.retentionLowerWarning(next), destructive: true });
            if (!ok) return;
          }
          try {
            await update.mutateAsync({ clientAbsentDays: days, retentionDays });
            setDraft(null);
            setRetentionDraft(null);
            toast.show(m.saved);
          } catch (err) {
            toast.show(err instanceof Error ? err.message : m.saveFailed, 'destructive');
          }
        }}
      >
        <h2 className="text-sm font-semibold">{m.title}</h2>
        {settings.isError ? (
          <p role="alert" className="text-xs text-destructive">
            {m.loadFailed}
          </p>
        ) : (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="client_absent_days">{m.clientAbsentDays}</Label>
            <Input
              id="client_absent_days"
              type="number"
              inputMode="numeric"
              min={0}
              max={MAX_DAYS}
              step={1}
              className="w-28"
              value={text}
              readOnly={!canWrite}
              disabled={settings.isPending}
              aria-invalid={invalid || undefined}
              aria-describedby={invalid ? 'client_absent_days_hint client_absent_days_error' : 'client_absent_days_hint'}
              onChange={(e) => {
                setDraft(e.target.value);
                setInvalid(false);
              }}
            />
            <span id="client_absent_days_hint" className="text-xs text-muted-foreground">
              {m.clientAbsentDaysHint}
            </span>
            {invalid && (
              <span id="client_absent_days_error" role="alert" className="text-[11px] text-destructive">
                {m.invalid}
              </span>
            )}
            {!canWrite && <span className="text-xs text-muted-foreground">{m.readOnlyHint}</span>}
          </div>
        )}
        {!settings.isError && maxDays !== undefined && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="retention_days">{m.retention}</Label>
            <Select
              id="retention_days"
              options={retentionOptions}
              value={retentionValue}
              onChange={(v) => setRetentionDraft(v)}
              disabled={!canWrite}
              aria-describedby="retention_days_hint"
            />
            <span id="retention_days_hint" className="text-xs text-muted-foreground">
              {m.retentionHint}
            </span>
          </div>
        )}
        {canWrite && !settings.isError && (
          <div className="flex justify-end">
            <Button type="submit" variant="primary" aria-label={m.saveLabel} disabled={settings.isPending || update.isPending || !dirty}>
              {m.save}
            </Button>
          </div>
        )}
      </form>
    </Card>
  );
}
