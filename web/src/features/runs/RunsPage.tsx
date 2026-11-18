import { useState } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Inbox, Terminal } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { Select } from '../../ui/Select.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useJobsPage } from '../../api/hooks/useJobs.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useSchedules } from '../../api/hooks/useSchedules.ts';
import { usePendingRunRequestCount } from '../../api/hooks/useRunRequests.ts';
import type { Job } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { AdhocDialog } from './AdhocDialog.tsx';
import { HostFilterChip } from './HostFilterChip.tsx';
import { runColumns } from './runColumns.tsx';
import { RUN_KINDS, RUN_STATUSES, RUNS_FILTERS } from './runsSearch.ts';
import { isCancellable, kindLabel, shortId, statusLabel } from './status.ts';
import { TRIGGERS, triggerLabel } from './trigger.ts';
import { useCancelRuns } from './useCancelRun.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunsPage is the org's run list (spec §7, backlog 2.12; list id `runs`):
 * sorted and paged by the server, filterable by status (`active` for the
 * live runs), kind, trigger and schedule (spec 2026-09-18 §8). The filters
 * live in the URL (?status=&kind=&trigger=&schedule=&host=, runsSearch.ts),
 * validated by the route; any change starts again at page 1. The host filter (the runs that targeted one host, linked from
 * the host view and the hosts list's Active runs) shows as a removable
 * chip. Each row opens its run; its menu offers Open and, for a live run
 * and a role that may run jobs, Cancel run (no confirmation, as on the run
 * page). Retry stays on the run page: the list does not carry the per-host
 * stats it depends on.
 */
export function RunsPage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const { filters: search, setFilter, setFilters, active, reset } = useListFilters(RUNS_FILTERS);
  const filter = { status: search.status, kind: search.kind, trigger: search.trigger, triggerRef: search.schedule, host: search.host };
  const schedules = useSchedules(search.trigger === 'schedule' ? org : '');
  const orgInfo = useOrg(org);
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const [adhocOpen, setAdhocOpen] = useState(false);
  const pendingRequests = usePendingRunRequestCount(org);
  const cancel = useCancelRuns(org);

  const columns = runColumns(t, fmt);
  const view = useListView('runs', columns, { mode: 'server', resetKey: JSON.stringify(filter) });
  const jobs = useJobsPage(org, filter, view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(jobs);
  if (guard) return guard;

  /** A new trigger drops the schedule filter unless it is still "schedule":
   * a schedule filter only makes sense for scheduled runs. */
  function filterTrigger(value: string) {
    const trigger = (value || undefined) as typeof search.trigger;
    setFilters({ trigger, schedule: trigger === 'schedule' ? search.schedule : undefined });
  }

  function rowActions(job: Job): RowAction[] {
    const items: RowAction[] = [
      { id: 'open', label: t.common.open, onSelect: () => void navigate({ to: '/o/$org/runs/$id', params: { org, id: job.id } }) },
    ];
    if (canRun && isCancellable(job.status)) {
      items.push({ id: 'cancel', label: t.runs.list.cancelItem, onSelect: () => void cancel.cancel(job.id), disabled: cancel.isPending(job.id) });
    }
    return items;
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-xl font-semibold">{t.runs.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">{t.runs.list.subtitle}</span>
        </div>
        <div className="flex gap-2">
          <Link to="/o/$org/runs/requests" params={{ org }} className={buttonClass()}>
            <Inbox size={15} />{' '}
            {pendingRequests ? t.runs.requests.openCount(pendingRequests) : t.runs.requests.open}
          </Link>
          {canRun && (
            <Button variant="primary" onClick={() => setAdhocOpen(true)}>
              <Terminal size={15} /> {t.runs.list.runCommand}
            </Button>
          )}
        </div>
      </div>
      <DataList
        label={t.runs.list.label}
        columns={columns}
        rows={jobs.data?.jobs ?? []}
        rowKey={(j) => j.id}
        rowLink={(j) => ({ to: '/o/$org/runs/$id', params: { org, id: j.id }, label: shortId(j.id) })}
        rowActions={rowActions}
        loading={jobs.isPending || jobs.isPlaceholderData}
        empty={t.runs.list.empty}
        toolbar={
          <ListToolbar>
            <Select
              aria-label={t.runs.list.filterStatus}
              options={[
                { value: 'active', label: t.runs.list.statusActive },
                ...RUN_STATUSES.map((s) => ({ value: s, label: statusLabel(s, t) })),
              ]}
              value={search.status ?? ''}
              onChange={(value) => setFilter('status', (value || undefined) as typeof search.status)}
              placeholder={t.runs.list.allStatuses}
            />
            <Select
              aria-label={t.runs.list.filterKind}
              options={RUN_KINDS.map((k) => ({ value: k, label: kindLabel(k, t) }))}
              value={search.kind ?? ''}
              onChange={(value) => setFilter('kind', (value || undefined) as typeof search.kind)}
              placeholder={t.runs.list.allKinds}
            />
            <Select
              aria-label={t.runs.list.filterTrigger}
              options={TRIGGERS.map((k) => ({ value: k, label: triggerLabel(k, t) }))}
              value={search.trigger ?? ''}
              onChange={filterTrigger}
              placeholder={t.runs.list.allTriggers}
            />
            {search.trigger === 'schedule' && (
              <Select
                aria-label={t.runs.list.filterSchedule}
                options={(schedules.data?.schedules ?? []).map((sc) => ({ value: sc.id, label: sc.name }))}
                value={search.schedule ?? ''}
                onChange={(value) => setFilter('schedule', value || undefined)}
                placeholder={t.runs.list.allSchedules}
              />
            )}
            {search.host && <HostFilterChip org={org} hostId={search.host} onRemove={() => setFilter('host', undefined)} />}
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(jobs.isPlaceholderData ? undefined : jobs.data?.total, t.runs.list.count)}
      />
      {adhocOpen && <AdhocDialog org={org} onClose={() => setAdhocOpen(false)} />}
    </>
  );
}
