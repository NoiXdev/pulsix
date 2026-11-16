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
  org, template, onStarted, onClose,
}: {
  org: string;
  template: Template;
  /** Called with the started run's jobs; the dialog closes after it. */
  onStarted: (jobs: { id: string }[]) => void | Promise<void>;
  onClose: () => void;
}) {
  const t = useT();
  const run = useRunTemplateById(org);
  const own = followUpIds(template);
  const [selection, setSelection] = useState(own);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const { jobs } = await run.mutateAsync({ id: template.id, followUps: sameIds(selection, own) ? undefined : selection });
      await onStarted(jobs ?? []);
      onClose();
    } catch (err) {
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
          <Button type="submit" variant="primary" disabled={run.isPending}>
            {t.templates.followUps.run}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
