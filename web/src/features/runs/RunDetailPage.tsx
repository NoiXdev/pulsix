import { Link, useParams } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useJob, useRunLog, isTerminal } from '../../api/hooks/useJobs.ts';
import { may } from '../../lib/roles.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { EventLog } from './EventLog.tsx';
import { RunSidebar } from './RunSidebar.tsx';
import { RunResultSection } from './RunResultSection.tsx';
import { RunGroupSection } from './RunGroupSection.tsx';
import { RunVaultSection } from './RunVaultSection.tsx';
import { shortId } from './status.ts';
import { triggerLabel } from './trigger.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunDetailPage is the run view (spec §7, backlog 2.12): the job's header,
 * its per-host results, the live event log — EventSource on the org-scoped
 * /jobs/{id}/stream with the seq-gap polling fallback (useRunLog) — the
 * sibling jobs of its run group, and the operator's cancel / retry-failed
 * actions (retry only when the run actually has failed hosts).
 */
export function RunDetailPage() {
  const t = useT();
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
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

  const guard = useQueryGuard(job);
  if (guard) return guard;
  if (!job.data) return <QueryLoading />;
  const j = job.data.job;

  // The run log's SSE status frame may carry the status before the detail
  // poll catches up; the header shows whichever is fresher.
  const status = log.status ?? j.status;
  const siblings = j.group ?? [];
  // Empty for a run whose repository uses no encrypted files at all.
  const vaultIdentities = j.vault_identities ?? [];

  return (
    <>
      <RunSidebar org={org} job={j} status={status} canRun={canRun} />
      {j.error && (
        <div role="alert" className="rounded-md border p-3 text-xs" style={{ borderColor: 'color-mix(in srgb, var(--px-crit) 33%, transparent)', background: 'color-mix(in srgb, var(--px-crit) 10%, transparent)', color: 'var(--px-crit)' }}>
          {j.error}
        </div>
      )}
      <Card>
        <div className="grid grid-cols-3 gap-4 p-4 text-xs">
          <Fact label={t.runs.detail.zone} value={shortId(j.zone_id)} />
          <Fact label={t.runs.detail.worker} value={j.worker_name ?? '—'} />
          <div className="flex flex-col gap-0.5">
            <span className="text-[11px] text-muted-foreground">{t.runs.detail.triggeredBy}</span>
            {j.trigger === 'schedule' && j.trigger_ref ? (
              <Link to="/o/$org/schedules" params={{ org }} search={{ id: j.trigger_ref, template: undefined }} className="truncate font-mono text-foreground hover:text-primary">
                {j.actor_name ? `${j.actor_name} · ${t.runs.trigger.schedule}` : t.runs.trigger.schedule}
              </Link>
            ) : (
              <span className="truncate font-mono text-foreground">{j.actor_name ?? triggerLabel(j.trigger, t)}</span>
            )}
          </div>
          <Fact label={t.runs.detail.started} value={j.started_at ? j.started_at.slice(0, 19).replace('T', ' ') : '—'} />
          <Fact label={t.runs.detail.finished} value={j.finished_at ? j.finished_at.slice(0, 19).replace('T', ' ') : '—'} />
          <Fact label={t.runs.detail.runGroup} value={t.runs.detail.groupCount(siblings.length + 1)} />
        </div>
      </Card>
      {vaultIdentities.length > 0 && <RunVaultSection identities={vaultIdentities} recorded={j.vault_identities_source === 'recorded'} />}
      {siblings.length > 0 && <RunGroupSection org={org} siblings={siblings} />}
      <RunResultSection job={j} status={status} events={log.events} />
      <Card>
        <div className="border-b border-border px-4 py-2 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
          {t.runs.detail.eventLog} {log.transport === 'poll' && <span title={t.runs.detail.pollingTitle}>{t.runs.detail.polling}</span>}
        </div>
        <EventLog events={log.events} />
      </Card>
    </>
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
