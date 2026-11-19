import { Link } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { useJobs } from '../../api/hooks/useJobs.ts';
import type { Template } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { statusLabel, statusTone } from '../runs/status.ts';
import { SidePanel } from './PlaybookSidePanels.tsx';

/** How many runs the side column shows; the Runs tab has them all. */
const LATEST = 3;

/**
 * PlaybookLatestRuns is the side column's last few runs of the playbook (the
 * runs list's `playbook_id` filter), each a link to its run, with a way to
 * the Runs tab.
 */
export function PlaybookLatestRuns({ org, playbookId, templates, onShowAll }: { org: string; playbookId: string; templates: readonly Template[]; onShowAll: () => void }) {
  const t = useT();
  const l = t.playbooks.page.latest;
  const fmt = useFormat();
  const jobs = useJobs(org, { playbook: playbookId, limit: LATEST });
  const rows = (jobs.data?.pages[0]?.jobs ?? []).slice(0, LATEST);
  const templateName = (id: string | undefined) => templates.find((tpl) => tpl.id === id)?.name ?? l.adhoc;

  return (
    <SidePanel
      title={l.title}
      action={
        <button type="button" onClick={onShowAll} className="text-xs text-muted-foreground hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
          {l.all}
        </button>
      }
    >
      {jobs.isError && (
        <p role="alert" className="text-xs text-destructive">
          {l.readFailed}
        </p>
      )}
      {jobs.data && rows.length === 0 && <p className="text-xs text-muted-foreground">{l.none}</p>}
      {rows.length > 0 && (
        <ul className="flex flex-col gap-1.5">
          {rows.map((job) => (
            <li key={job.id}>
              <Link
                to="/o/$org/runs/$id"
                params={{ org, id: job.id }}
                className="flex min-w-0 items-center gap-2 rounded text-xs hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              >
                <Pill tone={statusTone(job.status)}>{statusLabel(job.status, t)}</Pill>
                <span className="min-w-0 flex-1 truncate">{templateName(job.template_id)}</span>
                <span className="shrink-0 text-muted-foreground">{fmt.relative(job.created_at)}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </SidePanel>
  );
}
