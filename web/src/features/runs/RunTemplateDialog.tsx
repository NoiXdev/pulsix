import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useRunTemplateById, useTemplates } from '../../api/hooks/useTemplates.ts';
import type { Host } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunTemplateDialog runs one of the organization's job templates on a
 * single host (backlog 7.76): the host page's and the host row menu's
 * "Run template…". The server narrows the run to the host and refuses it
 * when the host is not among the template's targets or the template is not
 * ready; that message is shown inline and the dialog stays open, so the
 * person can pick another template. On success the dialog navigates to the
 * run view of the first job, as AdhocDialog does.
 */
export function RunTemplateDialog({ org, host, onClose }: { org: string; host: Host; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const templates = useTemplates(org);
  const run = useRunTemplateById(org);
  const [templateId, setTemplateId] = useState('');
  const [error, setError] = useState<string | null>(null);
  // Set from the first press until the run failed or the dialog closed: a
  // run that succeeded leaves the dialog open while the route changes, and
  // a second press in that window would start a second run.
  const [submitting, setSubmitting] = useState(false);

  const list = templates.data?.templates ?? [];
  const empty = !!templates.data && list.length === 0;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!templateId || submitting) return;
    setError(null);
    setSubmitting(true);
    try {
      const { jobs } = await run.mutateAsync({ id: templateId, hostId: host.id });
      toast.show(t.templates.list.started(list.find((x) => x.id === templateId)?.name ?? ''));
      if (jobs && jobs.length > 0) {
        await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
      }
      onClose();
    } catch (err) {
      setSubmitting(false);
      setError(err instanceof Error ? err.message : t.runs.templateRun.failed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={t.runs.templateRun.title(host.name)}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor="run-template">{t.runs.templateRun.template}</Label>
          <Select
            id="run-template"
            options={list.map((tpl) => ({ value: tpl.id, label: tpl.name }))}
            value={templateId}
            onChange={(id) => {
              setTemplateId(id);
              setError(null);
            }}
            placeholder={t.runs.templateRun.choose}
            disabled={empty}
          />
          {empty && <span className="text-[11px] text-muted-foreground">{t.runs.templateRun.none}</span>}
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={!templateId || submitting}>
            {t.runs.templateRun.run}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
