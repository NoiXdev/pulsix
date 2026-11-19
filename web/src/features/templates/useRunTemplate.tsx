import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { Template } from '../../api/types.ts';
import { useRunTemplateById } from '../../api/hooks/useTemplates.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import { RunWithFollowUpsDialog } from './RunWithFollowUpsDialog.tsx';

/**
 * useRunTemplate is how a person starts a job template: a template with
 * follow-ups asks first, in the "run with follow-ups" dialog (`dialog`), so
 * they can be changed for this run; any other one starts at once. Either way
 * a toast says so and the page moves to the first job's run view. The
 * templates list's Run and the playbook page's Run… share it.
 */
export function useRunTemplate(org: string): { run: (tpl: Template) => Promise<void>; isPending: boolean; dialog: ReactNode } {
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const runTemplate = useRunTemplateById(org);
  const [asking, setAsking] = useState<Template | null>(null);

  async function opened(tpl: Template, jobs: { id: string }[]) {
    toast.show(t.templates.list.started(tpl.name));
    if (jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
  }

  async function run(tpl: Template) {
    if ((tpl.follow_ups ?? []).length > 0) return setAsking(tpl);
    try {
      const { jobs } = await runTemplate.mutateAsync({ id: tpl.id });
      await opened(tpl, jobs ?? []);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.templates.list.startFailed, 'destructive');
    }
  }

  return {
    run,
    isPending: runTemplate.isPending,
    dialog: asking && (
      <RunWithFollowUpsDialog org={org} template={asking} onStarted={(jobs) => opened(asking, jobs)} onClose={() => setAsking(null)} />
    ),
  };
}
