import type { ReactNode } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCancelJob, useJob, useRetryFailedJobs, useRunLog, isTerminal } from '../../api/hooks/useJobs.ts';
import { may } from '../../lib/roles.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { EventLog } from './EventLog.tsx';
import { cancelHostRows, hasFailedHosts, hostResultRows, kindLabel, shortId, statusTone } from './status.ts';
import type { CancelHostState } from './status.ts';
import { triggerLabel } from './trigger.ts';

const RUNNING = new Set(['queued', 'claimed', 'running']);

// cancelHostRows answers a question about the host's STEP, not about the
// play: `finished` there means "this host's last event closed its step",
// which in a linear-strategy play is just as true after task 3 of 10 as
// after the last one. Labelling that bare "finished" invited an operator
// to re-run only the interrupted host and leave a half-applied one alone,
// so the label says what was actually observed.
const CANCEL_STATE_LABEL: Record<CancelHostState, string> = {
  finished: 'no task in flight',
  interrupted: 'interrupted',
  'never-started': 'never started',
};

const CANCEL_STATE_COLOR: Record<CancelHostState, string> = {
  finished: 'var(--px-unknown)',
  interrupted: 'var(--px-warn)',
  'never-started': 'var(--px-unknown)',
};

/**
 * RunDetailPage is the run view (spec §7, backlog 2.12): the job's header,
 * its per-host results, the live event log — EventSource on the org-scoped
 * /jobs/{id}/stream with the seq-gap polling fallback (useRunLog) — the
 * sibling jobs of its run group, and the operator's cancel / retry-failed
 * actions (retry only when the run actually has failed hosts).
 */
export function RunDetailPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const navigate = useNavigate();
  const toast = useToast();
  // The detail poll keeps a live run's header (status, stats) fresh; the
  // event log has its own SSE stream (useRunLog).
  const job = useJob(org, id, {
    refetchInterval: (q) => {
      const data = q.state.data as { job?: { status?: string } } | undefined;
      return data && !isTerminal(data.job?.status) ? 2000 : false;
    },
  });
  const log = useRunLog(org, id);
  const orgInfo = useOrg(org);
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');

  const cancel = useCancelJob(org, id);
  const retry = useRetryFailedJobs(org, id);

  const guard = useQueryGuard(job);
  if (guard) return guard;
  if (!job.data) return <QueryLoading />;
  const j = job.data.job;

  const rows = hostResultRows(j.stats);
  // The run log's SSE status frame may carry the status before the detail
  // poll catches up; the header shows whichever is fresher.
  const status = log.status ?? j.status;
  // A cancelled run gets no Ansible recap (the worker stops before
  // playbook_on_stats runs), so the per-host table above renders empty;
  // this derives what each target host was left doing from the event log
  // itself, which useRunLog seeds in full before anything else happens.
  const cancelRows = status === 'cancelled' ? cancelHostRows(j.hosts ?? [], log.events) : [];
  const retryable = isTerminal(j.status) && hasFailedHosts(j.stats);
  const cancellable = RUNNING.has(j.status);
  // A schedule's or webhook's per-run overrides are wiped when the run
  // ends (plan decision "Per-run overrides travel sealed on the job"), so a
  // retry uses the template's own limit and extra vars — said next to the
  // button, where the operator decides.
  const retryDropsOverrides = j.trigger === 'schedule' || j.trigger === 'webhook';
  const siblings = j.group ?? [];
  // Which labelled vault passwords this run actually received, and whether
  // each came from the repository or the template (decision 2's
  // template-wins precedence, internal/repos/vault.go's
  // ResolveVaultIdentities) — empty for a run whose repository uses no
  // encrypted files at all.
  const vaultIdentities = j.vault_identities ?? [];
  // "recorded" means this is what the claim actually resolved and stored
  // for this run (backlog 2.91); "computed" means there is no stored
  // record — a run still queued, one claimed before that recording
  // existed, or a job cancelled before it was ever claimed — so this is a
  // live read of the repository's and template's configuration right now,
  // which may no longer match what actually ran if either changed since.
  const vaultIdentitiesRecorded = j.vault_identities_source === 'recorded';

  async function onRetry() {
    try {
      const { jobs } = await retry.mutateAsync();
      toast.show('Retry started');
      if (jobs && jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Retry failed', 'destructive');
    }
  }

  async function onCancel() {
    try {
      const { job: cancelled } = await cancel.mutateAsync();
      toast.show(cancelled.status === 'cancelled' ? 'Run cancelled' : 'Cancellation requested');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Cancelling failed', 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <Link to="/o/$org/runs" params={{ org }} search={{ status: undefined, kind: undefined, trigger: undefined, schedule: undefined }} className="font-mono text-xl font-bold hover:text-primary">
            {shortId(j.id)}
          </Link>
          <Pill tone={statusTone(status)}>{status}</Pill>
          <span className="text-[13px] text-muted-foreground">{kindLabel(j.kind)}</span>
          {j.template && <span className="font-mono text-[13px]">{j.template.name}</span>}
        </div>
        {canRun && (
          <div className="flex items-center gap-2">
            {isTerminal(j.status) && retryDropsOverrides && (
              <span id="retry-overrides-note" className="max-w-xs text-right text-[11px] text-muted-foreground">
                Retry runs with the template&apos;s own limit and extra vars: overrides from the {j.trigger} are not kept.
              </span>
            )}
            {cancellable && (
              <Button type="button" onClick={() => void onCancel()} disabled={cancel.isPending}>
                Cancel run
              </Button>
            )}
            {isTerminal(j.status) && (
              <Button
                type="button"
                variant="primary"
                disabled={!retryable || retry.isPending}
                aria-describedby={retryDropsOverrides ? 'retry-overrides-note' : undefined}
                onClick={() => void onRetry()}
              >
                Retry failed
              </Button>
            )}
          </div>
        )}
      </div>
      {j.error && (
        <div role="alert" className="rounded-md border p-3 text-xs" style={{ borderColor: 'color-mix(in srgb, var(--px-crit) 33%, transparent)', background: 'color-mix(in srgb, var(--px-crit) 10%, transparent)', color: 'var(--px-crit)' }}>
          {j.error}
        </div>
      )}
      <Card>
        <div className="grid grid-cols-3 gap-4 p-4 text-xs">
          <Fact label="Zone" value={shortId(j.zone_id)} />
          <Fact label="Worker" value={j.worker_name ?? '—'} />
          <div className="flex flex-col gap-0.5">
            <span className="text-[11px] text-muted-foreground">Triggered by</span>
            {j.trigger === 'schedule' && j.trigger_ref ? (
              <Link to="/o/$org/schedules" params={{ org }} search={{ id: j.trigger_ref, template: undefined }} className="truncate font-mono text-foreground hover:text-primary">
                {j.actor_name ? `${j.actor_name} · Schedule` : 'Schedule'}
              </Link>
            ) : (
              <span className="truncate font-mono text-foreground">{j.actor_name ?? triggerLabel(j.trigger)}</span>
            )}
          </div>
          <Fact label="Started" value={j.started_at ? j.started_at.slice(0, 19).replace('T', ' ') : '—'} />
          <Fact label="Finished" value={j.finished_at ? j.finished_at.slice(0, 19).replace('T', ' ') : '—'} />
          <Fact label="Run group" value={`${siblings.length + 1} job${siblings.length === 0 ? '' : 's'}`} />
        </div>
      </Card>
      {vaultIdentities.length > 0 && (
        <Card>
          <div className="flex items-center justify-between gap-2 border-b border-border px-4 py-2">
            <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">Vault identities</span>
            <span
              className="text-[11px] text-muted-foreground"
              title={
                vaultIdentitiesRecorded
                  ? 'Recorded when this run was claimed; later changes to the repository or template do not affect it.'
                  : 'No record exists for this run yet, so this is read from the repository and template as they are configured right now.'
              }
            >
              {vaultIdentitiesRecorded ? 'recorded at claim time' : 'computed from current configuration'}
            </span>
          </div>
          <ul className="flex flex-col gap-1 p-4 text-xs" aria-label="Vault identities">
            {vaultIdentities.map((identity) => (
              <li key={identity.label} className="flex items-center justify-between gap-2">
                <span className="font-mono">{identity.label}</span>
                <span className="text-[11px] text-muted-foreground">{identity.from_template ? 'from template' : 'from repository'}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {siblings.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground" aria-label="Run group">
          <span>Same trigger:</span>
          {siblings.map((s) => (
            <Link key={s.id} to="/o/$org/runs/$id" params={{ org, id: s.id }} className="flex items-center gap-1.5 font-mono text-[13px] hover:text-primary">
              <span className="h-1.5 w-1.5 rounded-full" style={{ background: statusColor(s.status) }} />
              {shortId(s.id)}
            </Link>
          ))}
        </div>
      )}
      {rows.length > 0 && (
        <Card>
          <table className="w-full text-xs" aria-label="Per-host results">
            <thead>
              <tr className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
                <th className="px-4 py-2 text-left">Host</th>
                <th className="px-2 py-2">Ok</th>
                <th className="px-2 py-2">Changed</th>
                <th className="px-2 py-2">Failed</th>
                <th className="px-2 py-2">Unreachable</th>
                <th className="px-2 py-2">Skipped</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.host} className="border-t border-border">
                  <td className="px-4 py-1.5 font-mono text-[13px]">{r.host}</td>
                  <StateCell on={r.ok} color="var(--px-ok)" label={r.ok ? 'ok' : 'no'} />
                  <StateCell on={r.changed} color="var(--px-accent)" label={r.changed ? 'changed' : 'no'} />
                  <StateCell on={r.failed} color="var(--px-crit)" label={r.failed ? 'failed' : 'no'} />
                  <StateCell on={r.unreachable} color="var(--px-crit)" label={r.unreachable ? 'unreachable' : 'no'} />
                  <StateCell on={r.skipped} color="var(--px-unknown)" label={r.skipped ? 'skipped' : 'no'} />
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
      {cancelRows.length > 0 && (
        <Card>
          <div className="border-b border-border px-4 py-2 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
            Cancelled run
          </div>
          {/* Warn only where a command was in flight — when the worker
              that claimed the run said a cancel stops the play on it
              (cancel_stops_play, backlog 2.113). Backlog 2.107 made that
              true for a worker running plays under the play supervisor
              (spec 2026-10-29-cancel-stops-the-play-design.md, "What the
              run view shows"), so a host that was between tasks or never
              started has nothing left running and a banner there would be
              noise that trains operators to skip the one that matters. An
              interrupted host is the exception: the command already
              started on it is not undone by the stop.
              Without that word — an older worker, one without the
              supervisor, a run requeued since or claimed before the field
              existed — the play may still be running against every host,
              so the warning covers all of them, as it did before 2.107.
              The pill for a host with no task in flight is neutral, not
              green: "finished" there only says its last step closed, not
              that the play finished. */}
          {!j.cancel_stops_play ? (
            <CancelWarning>
              Pulsix cannot confirm that cancelling stopped the play: the worker that ran it may only stop following
              a cancelled run, and then the playbook may still be running against <strong>every host listed below</strong>,
              including those with no task in flight. A command already
              started on a host keeps running regardless. Treat this list as what Pulsix last saw, not as a final
              account: check the hosts before you re-run, or two runs may change the same host at once.
            </CancelWarning>
          ) : (
            cancelRows.some((r) => r.state === 'interrupted') && (
              <CancelWarning>
                The run was stopped. On a host marked interrupted, a command had already started there and may still
                be running on that host — stopping the run does not stop a command already running on a host. Check
                those hosts before you re-run.
              </CancelWarning>
            )
          )}
          <ul className="flex flex-col gap-1 p-4 text-xs" aria-label="Cancelled run hosts">
            {cancelRows.map((r) => (
              <li key={r.host} className="flex items-center justify-between gap-2">
                <span className="font-mono">{r.host}</span>
                <span className="flex items-center gap-2">
                  {r.state === 'interrupted' && <span className="text-muted-foreground">{r.task}</span>}
                  <span className="font-semibold" style={{ color: CANCEL_STATE_COLOR[r.state] }}>
                    {CANCEL_STATE_LABEL[r.state]}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Card>
        <div className="border-b border-border px-4 py-2 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
          Event log {log.transport === 'poll' && <span title="live stream dropped frames; polling the history">(polling)</span>}
        </div>
        <EventLog events={log.events} />
      </Card>
    </>
  );
}

function CancelWarning({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="m-4 rounded-md border p-3 text-xs"
      style={{ borderColor: 'color-mix(in srgb, var(--px-warn) 33%, transparent)', background: 'color-mix(in srgb, var(--px-warn) 10%, transparent)', color: 'var(--px-warn)' }}
    >
      {children}
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[11px] text-muted-foreground">{label}</span>
      <span className="truncate font-mono text-foreground">{value}</span>
    </div>
  );
}

function StateCell({ on, color, label }: { on: boolean; color: string; label: string }) {
  return (
    <td className="px-2 py-1.5 text-center">
      {on ? (
        <span className="font-mono text-[11px] font-semibold" style={{ color }}>
          {label}
        </span>
      ) : (
        <span className="text-muted-foreground">·</span>
      )}
    </td>
  );
}

function statusColor(status: string): string {
  switch (status) {
    case 'succeeded':
      return 'var(--px-ok)';
    case 'failed':
    case 'error':
      return 'var(--px-crit)';
    case 'running':
      return 'var(--px-accent)';
    case 'cancelling':
      return 'var(--px-warn)';
    default:
      return 'var(--px-unknown)';
  }
}
