import { Link, useNavigate } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCancelJob, useRetryFailedJobs, isTerminal } from '../../api/hooks/useJobs.ts';
import type { JobDetail } from '../../api/types.ts';
import { hasFailedHosts, kindLabel, shortId, statusLabel, statusTone } from './status.ts';
import { useT } from '../../i18n/useT.ts';

const RUNNING = new Set(['queued', 'claimed', 'running']);

/**
 * RunSidebar is who the run is and what an operator can do with it: its
 * short id, status, kind and template, and the cancel / retry-failed
 * actions (retry only when the run actually has failed hosts).
 */
export function RunSidebar({ org, job, status, canRun }: { org: string; job: JobDetail; status: string; canRun: boolean }) {
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const cancel = useCancelJob(org, job.id);
  const retry = useRetryFailedJobs(org, job.id);

  const retryable = isTerminal(job.status) && hasFailedHosts(job.stats);
  const cancellable = RUNNING.has(job.status);
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

  async function onCancel() {
    try {
      const { job: cancelled } = await cancel.mutateAsync();
      toast.show(cancelled.status === 'cancelled' ? t.runs.detail.cancelled : t.runs.detail.cancelRequested);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.runs.detail.cancelFailed, 'destructive');
    }
  }

  return (
    <div className="flex items-center justify-between gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/o/$org/runs" params={{ org }} search={{ status: undefined, kind: undefined, trigger: undefined, schedule: undefined, host: undefined }} className="font-mono text-xl font-bold hover:text-primary">
          {shortId(job.id)}
        </Link>
        <Pill tone={statusTone(status)}>{statusLabel(status, t)}</Pill>
        <span className="text-[13px] text-muted-foreground">{kindLabel(job.kind, t)}</span>
        {job.template && <span className="font-mono text-[13px]">{job.template.name}</span>}
      </div>
      {canRun && (
        <div className="flex items-center gap-2">
          {isTerminal(job.status) && retryDropsOverrides && (
            <span id="retry-overrides-note" className="max-w-xs text-right text-[11px] text-muted-foreground">
              {t.runs.detail.retryNote(job.trigger)}
            </span>
          )}
          {cancellable && (
            <Button type="button" onClick={() => void onCancel()} disabled={cancel.isPending}>
              {t.runs.detail.cancel}
            </Button>
          )}
          {isTerminal(job.status) && (
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
      )}
    </div>
  );
}
