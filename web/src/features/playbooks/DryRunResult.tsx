import { useCallback, useEffect, useId, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { fetchJobEvents, isTerminal, useJob, useNewestJob, useRunLog } from '../../api/hooks/useJobs.ts';
import type { JobDetail, JobEvent } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { statusLabel } from '../runs/status.ts';
import { DryRunChanges, DryRunCounts } from './DryRunChanges.tsx';
import { groupChanges, groupCounts, groupStatus, type RunFeed } from './dryRunGroup.ts';
import { useQueuedLong } from './useQueuedLong.ts';

/** How many of a run group's jobs follow their log by a live stream at once;
 * the others poll. A browser holds only a handful of connections to one
 * server (six over HTTP/1.1), and the page needs some for its other reads. */
export const MAX_LIVE_STREAMS = 4;

/** A live run's detail is read this often (its log streams on its own). */
const DETAIL_POLL_MS = 2000;

/** The detail of a live job is polled; an ended one is read once. */
const pollWhileLive = (q: { state: { data?: unknown } }) => {
  const data = q.state.data as { job?: JobDetail } | undefined;
  return data && !isTerminal(data.job?.status) ? DETAIL_POLL_MS : false;
};

/**
 * DryRunResult is the Dry run tab's latest dry run (spec §4): the newest run
 * of this playbook with check_mode — or, once one was started from the tab,
 * that one — together with the rest of its run group (a template over hosts
 * in several zones starts one job per zone). It follows each live job the
 * way the run page does (the detail polled, the log streamed by
 * useRunLog): the counts summed over every job's hosts (the recap's
 * host_counts once there, one result per task and host from the events
 * until then), and each task that would change, per host, with its
 * differences.
 */
export function DryRunResult({ org, playbookId, startedId }: { org: string; playbookId: string; startedId: string | null }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  const headingId = useId();
  const newest = useNewestJob(org, { playbook: playbookId, checkMode: true }, { enabled: !startedId });
  const id = startedId ?? newest.data?.id ?? '';
  const job = useJob(org, id, { refetchInterval: pollWhileLive });

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
    body = <ShownGroup key={id} org={org} lead={job.data.job} />;
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

/**
 * ShownGroup is the lead job (the one found or started) with the other jobs
 * of its run group: a RunFeedReader per job reads it and reports here, and
 * the view folds what was read — a job not read yet counts with the status
 * the lead's group lists for it. "Open run" opens the lead, whose run page
 * lists the group.
 */
function ShownGroup({ org, lead }: { org: string; lead: JobDetail }) {
  const t = useT();
  const l = t.playbooks.page.dryRun.latest;
  const fmt = useFormat();
  const members = lead.group ?? [];
  const ids = [lead.id, ...members.map((m) => m.id)];
  const [feeds, setFeeds] = useState<Readonly<Record<string, RunFeed>>>({});
  const report = useCallback((f: RunFeed) => setFeeds((all) => (all[f.job.id] === f ? all : { ...all, [f.job.id]: f })), []);

  const read = ids.flatMap((id) => (feeds[id] ? [feeds[id]] : []));
  const statuses = ids.map((id) => feeds[id]?.status ?? members.find((m) => m.id === id)?.status ?? lead.status);
  const status = groupStatus(statuses);
  const waiting = useQueuedLong(statuses.includes('queued'), lead.created_at);
  const hosts = read.length > 0 ? read.reduce((n, f) => n + (f.job.hosts ?? []).length, 0) : (lead.hosts ?? []).length;
  // A zone whose job has not been read yet has hosts that are not counted.
  const unread = ids.length - read.length;
  const errors = [...new Set(read.map((f) => f.job.error).filter((e): e is string => !!e))];
  const facts = [fmt.relative(lead.created_at), statusLabel(status, t), lead.template?.name ?? '', unread > 0 && ids.length > 1 ? l.hostsSoFar(hosts, unread) : l.hosts(hosts), ids.length > 1 ? l.zones(ids.length) : ''].filter(Boolean);
  return (
    <>
      {ids.map((id, i) => (
        <RunFeedReader key={id} org={org} id={id} stream={i < MAX_LIVE_STREAMS} onRead={report} />
      ))}
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{facts.join(' · ')}</span>
        <Link to="/o/$org/runs/$id" params={{ org, id: lead.id }} className="font-medium text-primary hover:underline">
          {l.open}
        </Link>
      </div>
      {waiting && (
        <p role="status" className="text-xs" style={{ color: 'var(--px-warn)' }}>
          {l.waiting}
        </p>
      )}
      {!isTerminal(status) && status !== 'queued' && (
        <p role="status" className="text-xs text-muted-foreground">
          {l.live}
        </p>
      )}
      {errors.map((e) => (
        <p key={e} role="alert" className="text-xs text-destructive [overflow-wrap:anywhere]">
          {e}
        </p>
      ))}
      <DryRunCounts counts={groupCounts(read)} />
      <DryRunChanges changed={groupChanges(read)} status={status} />
    </>
  );
}

/** RunFeedReader reads one job of the group — its detail polled while it
 * is live, its log streamed (or, past MAX_LIVE_STREAMS, polled) — and
 * reports each new state; it renders nothing itself. */
function RunFeedReader({ org, id, stream, onRead }: { org: string; id: string; stream: boolean; onRead: (f: RunFeed) => void }) {
  const job = useJob(org, id, { refetchInterval: pollWhileLive });
  const detail = job.data?.job;
  const log = useRunLog(org, stream ? id : '');
  const polled = usePolledEvents(org, stream ? '' : id, !isTerminal(detail?.status));
  const status = (stream ? log.status : null) ?? detail?.status;
  const events = stream ? log.events : polled;
  useEffect(() => {
    if (detail && status) onRead({ job: detail, status, events });
  }, [detail, status, events, onRead]);
  return null;
}

const NO_EVENTS: readonly JobEvent[] = [];

/** usePolledEvents is a job's whole log, read again every DETAIL_POLL_MS
 * while the job `live`s, and once more when it ends. Off for an empty id. */
function usePolledEvents(org: string, id: string, live: boolean): readonly JobEvent[] {
  const query = useQuery({
    queryKey: ['dry-run-events', org, id, live],
    queryFn: () => fetchJobEvents(org, id, 0),
    enabled: !!org && !!id,
    refetchInterval: live ? DETAIL_POLL_MS : false,
  });
  return query.data ?? NO_EVENTS;
}
