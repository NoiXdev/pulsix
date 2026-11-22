import { useEffect, useId, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { isTerminal, useJob, useNewestJob, useRunLog } from '../../api/hooks/useJobs.ts';
import type { JobDetail, JobEvent } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { sumHostCounts } from '../runs/hostCounts.ts';
import { statusLabel } from '../runs/status.ts';
import { DryRunChanges, DryRunCounts } from './DryRunChanges.tsx';
import { countTaskResults } from './dryRunTasks.ts';

/** How long a dry run may sit queued before the page says why it might. */
const QUEUED_HINT_MS = 10_000;

/** A live run's detail is read this often (its log streams on its own). */
const DETAIL_POLL_MS = 2000;

/**
 * DryRunResult is the Dry run tab's latest dry run (spec §4): the newest run
 * of this playbook with check_mode — or, once one was started from the tab,
 * that one. It follows a live run the way the run page does (the detail
 * polled, the log streamed by useRunLog): the counts summed over the hosts
 * (the recap's host_counts once there, one result per task and host from
 * the events until then), and each
 * task that would change, per host, with its differences.
 */
export function DryRunResult({ org, playbookId, startedId }: { org: string; playbookId: string; startedId: string | null }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  const headingId = useId();
  const newest = useNewestJob(org, { playbook: playbookId, checkMode: true }, { enabled: !startedId });
  const id = startedId ?? newest.data?.id ?? '';
  const job = useJob(org, id, {
    refetchInterval: (q) => {
      const data = q.state.data as { job?: JobDetail } | undefined;
      return data && !isTerminal(data.job?.status) ? DETAIL_POLL_MS : false;
    },
  });
  const log = useRunLog(org, id);

  let body;
  if (!id) {
    body = newest.isError ? (
      <p role="alert" className="text-xs text-destructive">
        {l.readFailed}
      </p>
    ) : newest.isPending ? null : (
      <p className="text-xs text-muted-foreground">{l.none}</p>
    );
  } else if (job.isError) {
    body = (
      <p role="alert" className="text-xs text-destructive">
        {l.readFailed}
      </p>
    );
  } else if (job.data) {
    body = <ShownRun org={org} job={job.data.job} status={log.status ?? job.data.job.status} events={log.events} />;
  }

  return (
    <section aria-labelledby={headingId} className="flex min-w-0 flex-col gap-3 rounded-[10px] border border-border bg-card px-4 py-4">
      <h2 id={headingId} className="text-sm font-semibold">
        {l.title}
      </h2>
      {body}
    </section>
  );
}

function ShownRun({ org, job, status, events }: { org: string; job: JobDetail; status: string; events: readonly JobEvent[] }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  const fmt = useFormat();
  const final = isTerminal(status);
  const waiting = useQueuedLong(status, job.created_at);
  const counts = sumHostCounts(job.stats) ?? countTaskResults(events);
  const facts = [fmt.relative(job.created_at), statusLabel(status, t), job.template?.name ?? '', l.hosts((job.hosts ?? []).length)].filter(Boolean);
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{facts.join(' · ')}</span>
        <Link to="/o/$org/runs/$id" params={{ org, id: job.id }} className="font-medium text-primary hover:underline">
          {l.open}
        </Link>
      </div>
      {waiting && (
        <p role="status" className="text-xs" style={{ color: 'var(--px-warn)' }}>
          {l.waiting}
        </p>
      )}
      {!final && status !== 'queued' && (
        <p role="status" className="text-xs text-muted-foreground">
          {l.live}
        </p>
      )}
      {job.error && (
        <p role="alert" className="text-xs text-destructive [overflow-wrap:anywhere]">
          {job.error}
        </p>
      )}
      <DryRunCounts counts={counts} />
      <DryRunChanges events={events} final={final} />
    </>
  );
}

/** useQueuedLong says a run has been queued for QUEUED_HINT_MS or longer:
 * a dry run no worker takes (only workers that support check mode do). */
function useQueuedLong(status: string, createdAt: string): boolean {
  const queued = status === 'queued';
  const due = Date.parse(createdAt) + QUEUED_HINT_MS;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!queued || now >= due) return;
    const timer = setTimeout(() => setNow(Date.now()), due - now);
    return () => clearTimeout(timer);
  }, [queued, due, now]);
  return queued && now >= due;
}
