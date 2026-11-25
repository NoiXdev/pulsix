import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useRunTemplateById } from '../../api/hooks/useTemplates.ts';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { FollowUpsField } from './FollowUpsField.tsx';
import { followUpIds, sameIds } from './followUps.ts';

/**
 * RunWithFollowUpsDialog runs a template that has follow-ups: the field is
 * preselected with the template's list and may be changed for this run
 * only. The selection is sent only when it differs from the template's
 * (order-sensitive); deselecting all sends an empty list.
 */
export function RunWithFollowUpsDialog({
  org, template, enrollHostIds, onStarted, onClose,
}: {
  org: string;
  template: Template;
  /** The hosts the person confirmed the run enrols the agent on, for a
   * template with agent enrollment (useRunTemplate asked first). */
  enrollHostIds?: string[];
  /** Called with the started run's jobs; the dialog closes after it. */
  onStarted: (jobs: { id: string }[]) => void | Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const run = useRunTemplateById(org);
  // Frozen at mount: a refetch of the list must not turn an untouched
  // selection into an override.
  const [own] = useState(() => followUpIds(template));
  const [selection, setSelection] = useState(own);
  const [error, setError] = useState<string | null>(null);
  // Set from the first press until the run failed or the dialog closed: a
  // started run leaves the dialog open while the route changes, and a
  // second press in that window would start a second run.
  const [submitting, setSubmitting] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (submitting) return;
    setError(null);
    setSubmitting(true);
    try {
      const { jobs } = await run.mutateAsync({ id: template.id, followUps: sameIds(selection, own) ? undefined : selection, enrollHostIds });
      await onStarted(jobs ?? []);
      onClose();
    } catch (err) {
      setSubmitting(false);
      setError(err instanceof Error ? err.message : t.templates.followUps.runFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={t.templates.followUps.runTitle(template.name)}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <FollowUpsField org={org} id="run-follow-ups" value={selection} onChange={setSelection} excludeId={template.id} />
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={run.isPending || submitting}>
            {t.templates.followUps.run}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
