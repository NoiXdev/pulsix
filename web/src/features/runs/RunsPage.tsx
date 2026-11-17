import { useState } from 'react';
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Inbox, Terminal } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { Select } from '../../ui/Select.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useJobsPage } from '../../api/hooks/useJobs.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useSchedules } from '../../api/hooks/useSchedules.ts';
import { usePendingRunRequestCount } from '../../api/hooks/useRunRequests.ts';
import type { Job } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useListView } from '../lists/useListView.ts';
import { AdhocDialog } from './AdhocDialog.tsx';
import { HostFilterChip } from './HostFilterChip.tsx';
import { runColumns } from './runColumns.tsx';
import { isCancellable, kindLabel, shortId, statusLabel } from './status.ts';
import { TRIGGERS, triggerLabel } from './trigger.ts';
import { useCancelRuns } from './useCancelRun.ts';
import { useT } from '../../i18n/useT.ts';

const STATUSES = ['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'];
const KINDS = ['run', 'connection_test', 'bootstrap', 'rotate_key', 'revoke_key', 'sync_repo'];

interface RunsSearch {
  status?: string;
  kind?: string;
  trigger?: string;
  schedule?: string;
  host?: string;
}

/**
 * RunsPage is the org's run list (spec §7, backlog 2.12; list id `runs`):
 * sorted and paged by the server, filterable by status (`active` for the
 * live runs), kind, trigger and schedule (spec 2026-09-18 §8). The filters
 * live in the URL (?status=&kind=&trigger=&schedule=&host=), validated by
 * the route; the host filter (the runs that targeted one host, linked from
 * the host view and the hosts list's Active runs) shows as a removable
 * chip. Each row opens its run; its menu offers Open and, for a live run
 * and a role that may run jobs, Cancel run (no confirmation, as on the run
 * page). Retry stays on the run page: the list does not carry the per-host
 * stats it depends on.
 */
export function RunsPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as RunsSearch;
  const filter = { status: search.status, kind: search.kind, trigger: search.trigger, triggerRef: search.schedule, host: search.host };
  const schedules = useSchedules(search.trigger === 'schedule' ? org : '');
  const orgInfo = useOrg(org);
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const [adhocOpen, setAdhocOpen] = useState(false);
  const pendingRequests = usePendingRunRequestCount(org);
  const cancel = useCancelRuns(org);

  const columns = runColumns(t);
  const view = useListView('runs', columns, { mode: 'server', resetKey: JSON.stringify(filter) });
  const jobs = useJobsPage(org, filter, view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(jobs);
  if (guard) return guard;

  /** A new filter keeps the sort and starts again at page 1. */
  function navigateFilter(next: RunsSearch) {
    void navigate({
      to: '/o/$org/runs',
      params: { org },
      search: (prev) => ({ ...prev, ...next, page: undefined }),
    });
  }

  function filterBy(next: Omit<RunsSearch, 'host'>) {
    const trigger = next.trigger ?? search.trigger;
    navigateFilter({
      status: next.status ?? search.status,
      kind: next.kind ?? search.kind,
      trigger,
      // A schedule filter only makes sense for scheduled runs.
      schedule: trigger === 'schedule' ? (next.schedule ?? search.schedule) : undefined,
    });
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
          <>
            <Select
              aria-label={t.runs.list.filterStatus}
              options={[
                { value: 'active', label: t.runs.list.statusActive },
                ...STATUSES.map((s) => ({ value: s, label: statusLabel(s, t) })),
              ]}
              value={search.status ?? ''}
              onChange={(value) => filterBy({ status: value })}
              placeholder={t.runs.list.allStatuses}
            />
            <Select
              aria-label={t.runs.list.filterKind}
              options={KINDS.map((k) => ({ value: k, label: kindLabel(k, t) }))}
              value={search.kind ?? ''}
              onChange={(value) => filterBy({ kind: value })}
              placeholder={t.runs.list.allKinds}
            />
            <Select
              aria-label={t.runs.list.filterTrigger}
              options={TRIGGERS.map((k) => ({ value: k, label: triggerLabel(k, t) }))}
              value={search.trigger ?? ''}
              onChange={(value) => filterBy({ trigger: value })}
              placeholder={t.runs.list.allTriggers}
            />
            {search.trigger === 'schedule' && (
              <Select
                aria-label={t.runs.list.filterSchedule}
                options={(schedules.data?.schedules ?? []).map((sc) => ({ value: sc.id, label: sc.name }))}
                value={search.schedule ?? ''}
                onChange={(value) => filterBy({ schedule: value })}
                placeholder={t.runs.list.allSchedules}
              />
            )}
            {search.host && <HostFilterChip org={org} hostId={search.host} onRemove={() => navigateFilter({ host: undefined })} />}
          </>
        }
        {...view.listProps(jobs.isPlaceholderData ? undefined : jobs.data?.total, t.runs.list.count)}
      />
      {adhocOpen && <AdhocDialog org={org} onClose={() => setAdhocOpen(false)} />}
    </>
  );
}
