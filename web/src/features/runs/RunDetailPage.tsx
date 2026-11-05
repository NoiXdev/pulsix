import { useParams } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { DetailLayout, Section, type SectionNavItem } from '../../ui/detail-layout.tsx';
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
import { useT } from '../../i18n/useT.ts';

/**
 * RunDetailPage is the run view (spec §7, backlog 2.12), laid out like the
 * host page: a sticky sidebar with the run's status, facts and the
 * operator's cancel / retry-failed actions, and the sections Result, Run
 * group (only with sibling jobs), Vault identities (only when the run has
 * any) and the live Event log — EventSource on the org-scoped
 * /jobs/{id}/stream with the seq-gap polling fallback (useRunLog). The log
 * comes last because it grows while the run is live.
 */
export function RunDetailPage() {
  const t = useT();
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  // The detail poll keeps a live run's status and stats fresh; the event
  // log has its own SSE stream (useRunLog).
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
  // poll catches up; the page shows whichever is fresher.
  const status = log.status ?? j.status;
  const siblings = j.group ?? [];
  // Empty for a run whose repository uses no encrypted files at all.
  const vaultIdentities = j.vault_identities ?? [];
  const s = t.runs.detail.section;

  const nav: SectionNavItem[] = [
    { id: 'result', label: s.result },
    ...(siblings.length > 0 ? [{ id: 'group', label: s.group, count: siblings.length + 1 }] : []),
    ...(vaultIdentities.length > 0 ? [{ id: 'vault', label: t.credentials.vault.heading }] : []),
    { id: 'log', label: s.log },
  ];

  return (
    <DetailLayout sidebar={<RunSidebar org={org} job={j} status={status} canRun={canRun} />} nav={nav}>
      <RunResultSection job={j} status={status} events={log.events} />
      {siblings.length > 0 && <RunGroupSection org={org} siblings={siblings} />}
      {vaultIdentities.length > 0 && <RunVaultSection identities={vaultIdentities} recorded={j.vault_identities_source === 'recorded'} />}
      <Section
        id="log"
        title={s.log}
        actions={
          log.transport === 'poll' && (
            <span className="text-[11px] text-muted-foreground" title={t.runs.detail.pollingTitle}>
              {t.runs.detail.polling}
            </span>
          )
        }
      >
        {/* No height cap and no inner scroller: the log grows with the page,
            which is the one scroller (EventLog only scrolls long lines
            sideways). */}
        <Card>
          <EventLog events={log.events} />
        </Card>
      </Section>
    </DetailLayout>
  );
}
