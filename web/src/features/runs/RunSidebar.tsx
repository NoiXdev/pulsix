import { Link, useNavigate } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SidebarFacts, SidebarHeading } from '../../ui/detail-layout.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { fetchRetryEnrollment, useDismissFollowUps, useRetryFailedJobs, isTerminal } from '../../api/hooks/useJobs.ts';
import { useConfirmEnrollment } from './useConfirmEnrollment.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import type { JobDetail } from '../../api/types.ts';
import { hasFailedHosts, isCancellable, kindLabel, shortId, statusLabel, statusTone, zoneSlug } from './status.ts';
import { chainTemplateName } from './chainTemplateName.ts';
import { triggerLabel } from './trigger.ts';
import { useCancelRun } from './useCancelRun.ts';
import { CommitId } from './CommitId.tsx';
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
  const dismiss = useDismissFollowUps(org, job.id);
  const confirm = useConfirm();
  const confirmEnrollment = useConfirmEnrollment();

  const terminal = isTerminal(status);
  const retryable = terminal && hasFailedHosts(job.stats);
  const cancellable = isCancellable(status);
  // A schedule's or webhook's per-run overrides are wiped when the run
  // ends (plan decision "Per-run overrides travel sealed on the job"), so a
  // retry uses the template's own limit and extra vars — said next to the
  // button, where the operator decides.
  const retryDropsOverrides = job.trigger === 'schedule' || job.trigger === 'webhook';

  const parent = job.chain?.parent ?? null;
  const hasPending = (job.chain?.follow_ups ?? []).some((f) => f.state === 'pending');
  const dismissable = canRun && !terminal && hasPending;

  async function onDismiss() {
    const c = t.runs.detail.chain;
    if (!(await confirm({ message: c.dismissConfirm, confirmLabel: c.dismiss }))) return;
    try {
      const { dismissed } = await dismiss.mutateAsync();
      toast.show(c.dismissed(dismissed));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : c.dismissFailed, 'destructive');
    }
  }

  async function onRetry() {
    try {
      // A retry of a template with agent enrollment: the person confirms
      // the failed hosts it enrols the agent on (spec 2026-11-25 B).
      const enrollHostIds = await confirmEnrollment(await fetchRetryEnrollment(org, job.id));
      if (enrollHostIds === null) return;
      const { jobs } = await retry.mutateAsync(enrollHostIds);
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
        dot={{ tone: statusTone(status, job.withdrawn_at_claim), label: statusLabel(status, t, job.withdrawn_at_claim) }}
        pill={<Pill tone={statusTone(status, job.withdrawn_at_claim)}>{statusLabel(status, t, job.withdrawn_at_claim)}</Pill>}
      >
        <span>{kindLabel(job.kind, t)}</span>
        {job.template && (
          <>
            {' · '}
            <span className="font-mono">{job.template.name}</span>
          </>
        )}
      </SidebarHeading>
      {parent && (
        <p className="text-xs text-muted-foreground">
          {t.runs.detail.chain.parentOf}{' '}
          <Link to="/o/$org/runs/$id" params={{ org, id: parent.first_job_id }} className="font-mono hover:text-primary">
            {chainTemplateName(parent.template_name, t)}
          </Link>
        </p>
      )}
      <RunFacts org={org} job={job} />
      {/* A pinned run waits for a worker that checks out its commit; an
          older one never takes it (spec 2026-11-25 A). */}
      {job.pinned_commit && status === 'queued' && (
        <p role="note" aria-label={t.runs.detail.pinnedCommit} className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs text-[var(--px-warn)]">
          {t.runs.detail.pinnedWaiting}
        </p>
      )}
      {dismissable && (
        <div>
          <Button type="button" onClick={() => void onDismiss()} disabled={dismiss.isPending}>
            {t.runs.detail.chain.dismiss}
          </Button>
        </div>
      )}
      {canRun && (cancellable || (terminal && !job.check_mode)) && (
        <div className="flex flex-col items-start gap-2">
          <div className="flex flex-wrap items-center gap-2">
            {cancellable && (
              <Button type="button" onClick={() => void cancel.cancel()} disabled={cancel.isPending}>
                {t.runs.detail.cancel}
              </Button>
            )}
            {/* A dry run is never retried: the retry would run its hosts for real. */}
            {terminal && !job.check_mode && (
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
          {terminal && !job.check_mode && retryDropsOverrides && (
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
 * finished, how many jobs its run group has and the commit it is pinned to. */
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
        // The commit an approved run runs instead of the branch tip (spec
        // 2026-11-25 A).
        ...(job.pinned_commit ? [{ label: t.runs.detail.pinnedCommit, value: <CommitId sha={job.pinned_commit} /> }] : []),
        // The agent enrollment tokens the run handed its hosts, as counts
        // (spec 2026-11-25 B): the tokens themselves are never served. A
        // run that minted none (a dry run, every host had an agent) shows
        // nothing.
        ...(job.enroll_tokens && job.enroll_tokens.minted > 0
          ? [{ label: t.runs.detail.enrollTokens, value: <span className="font-mono">{t.runs.detail.enrollTokenCounts(job.enroll_tokens.minted, job.enroll_tokens.used)}</span> }]
          : []),
      ]}
    />
  );
}

function timestamp(at: string | undefined | null): string {
  return at ? at.slice(0, 19).replace('T', ' ') : NONE;
}
