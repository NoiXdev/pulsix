import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { Template } from '../../api/types.ts';
import { fetchTemplateEnrollment, useRunTemplateById } from '../../api/hooks/useTemplates.ts';
import { useConfirmEnrollment } from '../runs/useConfirmEnrollment.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import { RunWithFollowUpsDialog } from './RunWithFollowUpsDialog.tsx';

/**
 * useRunTemplate is how a person starts a job template: a template with
 * agent enrollment first asks the person to confirm the hosts it enrols
 * the agent on (spec 2026-11-25 B); a template with follow-ups asks, in
 * the "run with follow-ups" dialog (`dialog`), so they can be changed for
 * this run; any other one starts at once. Either way a toast says so and
 * the page moves to the first job's run view. The templates list's Run and
 * the playbook page's Run… share it.
 */
export function useRunTemplate(org: string): { run: (tpl: Template) => Promise<void>; isPending: boolean; dialog: ReactNode } {
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const runTemplate = useRunTemplateById(org);
  const confirmEnrollment = useConfirmEnrollment();
  const [asking, setAsking] = useState<{ tpl: Template; enrollHostIds?: string[] } | null>(null);

  async function opened(tpl: Template, jobs: { id: string }[]) {
    toast.show(t.templates.list.started(tpl.name));
    if (jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
  }

  async function run(tpl: Template) {
    try {
      // The preview, not the cached template, decides whether to ask: a
      // template switched to enrolling since the list loaded still asks.
      const enrollHostIds = await confirmEnrollment(await fetchTemplateEnrollment(org, tpl.id));
      if (enrollHostIds === null) return;
      if ((tpl.follow_ups ?? []).length > 0) return setAsking({ tpl, enrollHostIds });
      const { jobs } = await runTemplate.mutateAsync({ id: tpl.id, enrollHostIds });
      await opened(tpl, jobs ?? []);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.templates.list.startFailed, 'destructive');
    }
  }

  return {
    run,
    isPending: runTemplate.isPending,
    dialog: asking && (
      <RunWithFollowUpsDialog
        org={org}
        template={asking.tpl}
        enrollHostIds={asking.enrollHostIds}
        onStarted={(jobs) => opened(asking.tpl, jobs)}
        onClose={() => setAsking(null)}
      />
    ),
  };
}
