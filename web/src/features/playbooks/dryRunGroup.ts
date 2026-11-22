import { isTerminal } from '../../api/hooks/useJobs.ts';
import type { JobDetail, JobEvent } from '../../api/types.ts';
import { sumHostCounts, ZERO_COUNTS, type RecapCounts } from '../runs/hostCounts.ts';
import { changedTasks, countTaskResults, COUNT_KEYS, type ChangedTask } from './dryRunTasks.ts';

/**
 * A dry run over hosts in several zones is one job per zone, all in one run
 * group; the Dry run tab shows the group as one result. A RunFeed is what
 * the tab has read of one of those jobs: its detail, its live status and
 * its events so far.
 */
export interface RunFeed {
  job: JobDetail;
  status: string;
  events: readonly JobEvent[];
}

/** Which ended status a group shows when its jobs ended differently: the
 * weightiest first. */
const ENDED_ORDER = ['error', 'failed', 'cancelled'] as const;

/** groupStatus is one status for a run group's jobs: live while any job is
 * (queued only while every live one is, else the one live status they share,
 * else running); once all ended, error over failed over cancelled over
 * succeeded. */
export function groupStatus(statuses: readonly string[]): string {
  const live = statuses.filter((s) => !isTerminal(s));
  if (live.length > 0) return live.every((s) => s === live[0]) ? live[0] : 'running';
  return ENDED_ORDER.find((s) => statuses.includes(s)) ?? 'succeeded';
}

/** groupCounts sums the counts over the jobs: each job's recap counts once
 * it has them, until then one result per task and host from its events. */
export function groupCounts(feeds: readonly RunFeed[]): RecapCounts {
  const out: RecapCounts = { ...ZERO_COUNTS };
  for (const f of feeds) {
    const c = sumHostCounts(f.job.stats) ?? countTaskResults(f.events);
    for (const k of COUNT_KEYS) out[k] += c[k];
  }
  return out;
}

/** groupChanges lists the changes of every job, job by job; each key names
 * its job, as two jobs' events share sequence numbers. */
export function groupChanges(feeds: readonly RunFeed[]): ChangedTask[] {
  return feeds.flatMap((f) => changedTasks(f.events).map((c) => ({ ...c, key: `${f.job.id}-${c.key}` })));
}
