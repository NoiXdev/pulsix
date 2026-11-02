import { useState, type FormEvent } from 'react';
import { useT } from '../../i18n/useT.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { useAcknowledgeAlert } from '../../api/hooks/useAlerts.ts';

const TEXTAREA =
  'min-h-24 w-full rounded-md border border-border bg-background p-2 text-xs text-foreground placeholder:text-muted-foreground';

export interface AcknowledgeDialogProps {
  org: string;
  alertId: string;
  onClose: () => void;
}

/**
 * AcknowledgeDialog records that an operator knows about one alert, with an
 * optional note — "an ack is not a mute" (design doc, "Suppression"): it
 * stops nothing, it only says a human has looked. The server stamps
 * `acknowledged_severity` itself, from the alert's own current severity at
 * the instant the write commits (service.go's own doc comment on
 * Acknowledge) — never a value this dialog could supply, since whatever it
 * read a moment ago could already be stale.
 */
export function AcknowledgeDialog({ org, alertId, onClose }: AcknowledgeDialogProps) {
  const t = useT();
  const acknowledge = useAcknowledgeAlert(org);
  const [note, setNote] = useState('');
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await acknowledge.mutateAsync({ id: alertId, body: note.trim() ? { note: note.trim() } : {} });
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.alerts.acknowledgeDialog.failed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={t.alerts.acknowledgeDialog.title}>
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="ack-note">{t.alerts.acknowledgeDialog.note}</Label>
        <textarea
          id="ack-note"
          className={TEXTAREA}
          maxLength={1000}
          placeholder={t.alerts.acknowledgeDialog.placeholder}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={acknowledge.isPending}>
            {t.alerts.row.acknowledge}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
