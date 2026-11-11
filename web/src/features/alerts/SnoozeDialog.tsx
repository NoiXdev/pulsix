import { useState, type FormEvent } from 'react';
import { useT } from '../../i18n/useT.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { DateTimePicker } from '../../ui/DateTimePicker.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useSnoozeAlert } from '../../api/hooks/useAlerts.ts';
import { fromLocalInput } from '../maintenance/time.ts';

export interface SnoozeDialogProps {
  org: string;
  alertId: string;
  onClose: () => void;
}

/**
 * SnoozeDialog mutes one open alert until an instant, or until its
 * severity next changes — never both, never neither (Service.Snooze's own
 * "never both, never neither" is the server's last word on this, but
 * nothing stops a round trip over something Save could have refused
 * itself). The two fields are mutually exclusive in the form, not just in
 * validation: picking a date clears the checkbox and picking the
 * checkbox clears the date, so the body this dialog builds can never
 * itself disagree with that rule.
 *
 * Save stays disabled while neither field is set — the one behaviour the
 * brief calls out by name — rather than only failing after a submit, so an
 * operator who has not yet chosen either sees an inert button instead of a
 * round trip that was always going to 422.
 */
export function SnoozeDialog({ org, alertId, onClose }: SnoozeDialogProps) {
  const t = useT();
  const snooze = useSnoozeAlert(org);
  const [until, setUntil] = useState('');
  const [untilChange, setUntilChange] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const neitherSet = until === '' && !untilChange;
  const disabled = snooze.isPending || neitherSet;

  function onUntilChange(value: string) {
    setUntil(value);
    if (value !== '') setUntilChange(false);
  }

  function onUntilChangeToggle(checked: boolean) {
    setUntilChange(checked);
    if (checked) setUntil('');
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (neitherSet) return;
    setError(null);
    try {
      await snooze.mutateAsync({
        id: alertId,
        body: untilChange ? { until_change: true } : { until: fromLocalInput(until) },
      });
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.alerts.snoozeDialog.failed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={t.alerts.snoozeDialog.title}>
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="snooze-until">{t.alerts.snoozeDialog.until}</Label>
        <DateTimePicker id="snooze-until" value={until} disabled={untilChange} onChange={onUntilChange} />
        <div className="flex items-center gap-2 text-xs text-foreground">
          <Switch id="snooze-until-change" checked={untilChange} onCheckedChange={onUntilChangeToggle} />
          <label htmlFor="snooze-until-change">{t.alerts.snoozeDialog.untilChange}</label>
        </div>
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={disabled}>
            {t.alerts.row.snooze}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
