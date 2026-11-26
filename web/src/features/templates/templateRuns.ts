import type { Job } from '../../api/types.ts';

interface Named {
  id: string;
  name: string;
}

/**
 * runSource names what started a run of the template when the template
 * page knows it: the schedule of a scheduled run, the webhook trigger of a
 * triggered one (the job's `trigger_ref`, looked up in the template's own
 * schedules and triggers). Undefined for every other run, for one whose
 * schedule or trigger is gone, and while the lists are read.
 */
export function runSource(
  job: Pick<Job, 'trigger' | 'trigger_ref'>,
  schedules: readonly Named[] | undefined,
  triggers: readonly Named[] | undefined,
): string | undefined {
  if (!job.trigger_ref) return undefined;
  const from = job.trigger === 'schedule' ? schedules : job.trigger === 'webhook' ? triggers : undefined;
  return from?.find((x) => x.id === job.trigger_ref)?.name;
}
