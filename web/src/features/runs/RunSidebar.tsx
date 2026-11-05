import { Link, useNavigate } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SidebarFacts, SidebarHeading } from '../../ui/detail-layout.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useRetryFailedJobs, isTerminal } from '../../api/hooks/useJobs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import type { JobDetail } from '../../api/types.ts';
import { hasFailedHosts, isCancellable, kindLabel, shortId, statusLabel, statusTone, zoneSlug } from './status.ts';
import { triggerLabel } from './trigger.ts';
import { useCancelRun } from './useCancelRun.ts';
import { useT } from '../../i18n/useT.ts';

const NONE = '—';

/**
 * RunSidebar is the run page's left column: the run's short id with its
 * status, kind and template, the facts (zone, worker, trigger, times, run
 * group) and, for a role that may run jobs, Cancel run while the run is live
 * and Retry failed once it has ended (enabled only when the run actually has
 * failed hosts). `status` is the fresher of the live log's status frame and
 * the detail poll, so the heading and the actions follow the stream.
 */
export function RunSidebar({ org, job, status, canRun }: { org: string; job: JobDetail; status: string; canRun: boolean }) {
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const cancel = useCancelRun(org, job.id);
  const retry = useRetryFailedJobs(org, job.id);

  const terminal = isTerminal(status);
  const retryable = terminal && hasFailedHosts(job.stats);
  const cancellable = isCancellable(status);
  // A schedule's or webhook's per-run overrides are wiped when the run
  // ends (plan decision "Per-run overrides travel sealed on the job"), so a
  // retry uses the template's own limit and extra vars — said next to the
  // button, where the operator decides.
  const retryDropsOverrides = job.trigger === 'schedule' || job.trigger === 'webhook';

  async function onRetry() {
    try {
      const { jobs } = await retry.mutateAsync();
      toast.show(t.runs.detail.retryStarted);
      if (jobs && jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.runs.detail.retryFailed, 'destructive');
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <SidebarHeading
        title={shortId(job.id)}
        mono
        dot={{ tone: statusTone(status), label: statusLabel(status, t) }}
        pill={<Pill tone={statusTone(status)}>{statusLabel(status, t)}</Pill>}
      >
        <span>{kindLabel(job.kind, t)}</span>
        {job.template && (
          <>
            {' · '}
            <span className="font-mono">{job.template.name}</span>
          </>
        )}
      </SidebarHeading>
      <RunFacts org={org} job={job} />
      {canRun && (cancellable || terminal) && (
        <div className="flex flex-col items-start gap-2">
          <div className="flex flex-wrap items-center gap-2">
            {cancellable && (
              <Button type="button" onClick={() => void cancel.cancel()} disabled={cancel.isPending}>
                {t.runs.detail.cancel}
              </Button>
            )}
            {terminal && (
              <Button
                type="button"
                variant="primary"
                disabled={!retryable || retry.isPending}
                aria-describedby={retryDropsOverrides ? 'retry-overrides-note' : undefined}
                onClick={() => void onRetry()}
              >
                {t.runs.detail.retry}
              </Button>
            )}
          </div>
          {terminal && retryDropsOverrides && (
            <span id="retry-overrides-note" className="text-[11px] text-muted-foreground">
              {t.runs.detail.retryNote(job.trigger)}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

/** Zone, worker, who or what triggered the run, when it started and
 * finished, and how many jobs its run group has. */
function RunFacts({ org, job }: { org: string; job: JobDetail }) {
  const t = useT();
  const zones = useZones(org);
  const siblings = job.group ?? [];
  // While the zones load the fact says so; a zone missing from the list (or
  // a list that cannot be read) falls back to a piece of its id.
  const zone = zones.isPending ? t.common.loading : zoneSlug(zones.data?.zones, job.zone_id);

  return (
    <SidebarFacts
      items={[
        { label: t.runs.detail.zone, value: <span className="font-mono">{zone}</span> },
        { label: t.runs.detail.worker, value: <span className="font-mono">{job.worker_name ?? NONE}</span> },
        {
          label: t.runs.detail.triggeredBy,
          value:
            job.trigger === 'schedule' && job.trigger_ref ? (
              <Link to="/o/$org/schedules" params={{ org }} search={{ id: job.trigger_ref, template: undefined }} className="font-mono hover:text-primary">
                {job.actor_name ? `${job.actor_name} · ${t.runs.trigger.schedule}` : t.runs.trigger.schedule}
              </Link>
            ) : (
              <span className="font-mono">{job.actor_name ?? triggerLabel(job.trigger, t)}</span>
            ),
        },
        { label: t.runs.detail.started, value: <span className="font-mono">{timestamp(job.started_at)}</span> },
        { label: t.runs.detail.finished, value: <span className="font-mono">{timestamp(job.finished_at)}</span> },
        { label: t.runs.detail.runGroup, value: t.runs.detail.groupCount(siblings.length + 1) },
      ]}
    />
  );
}

function timestamp(at: string | undefined | null): string {
  return at ? at.slice(0, 19).replace('T', ' ') : NONE;
}
