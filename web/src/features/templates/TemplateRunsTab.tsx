import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, DataList, type DataListColumn, type RowAction } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { useJobsPage } from '../../api/hooks/useJobs.ts';
import type { Job, Schedule, Trigger } from '../../api/types.ts';
import { useFormat, type Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListView } from '../lists/useListView.ts';
import { RunModeFilter } from '../runs/RunModeFilter.tsx';
import { RUN_STATUSES } from '../runs/runsSearch.ts';
import { isCancellable, kindLabel, shortId, statusLabel, statusTone } from '../runs/status.ts';
import { TRIGGERS, triggerLabel } from '../runs/trigger.ts';
import { useCancelRuns } from '../runs/useCancelRun.ts';
import { runSource } from './templateRuns.ts';

interface Filters {
  status?: string;
  trigger?: string;
  /** The dry runs only (true), the real runs only (false); both when unset. */
  checkMode?: boolean;
}

export interface TemplateRunsTabProps {
  org: string;
  templateId: string;
  role: string;
  /** The template's schedules and triggers, to name what started a run;
   * undefined while read. */
  schedules: readonly Schedule[] | undefined;
  triggers: readonly Trigger[] | undefined;
}

/**
 * TemplateRunsTab is the template page's Runs tab: the history of the
 * template's runs (the jobs list's own `template_id` filter), sorted and
 * paged by the server like the runs list, narrowed further by status,
 * trigger and mode. Each row is a run — when it was started, what started
 * it (the schedule or webhook trigger by name), the commit an approved run
 * was pinned to (none for a run of the branch tip), how many hosts it targeted and its result — and opens it;
 * its menu also cancels a live run. The page's URL holds only the tab, so
 * sort, page and filters live in the component (list id `template-runs`).
 */
export function TemplateRunsTab({ org, templateId, role, schedules, triggers }: TemplateRunsTabProps) {
  const t = useT();
  const fmt = useFormat();
  const r = t.templates.page.runs;
  const navigate = useNavigate();
  const cancel = useCancelRuns(org);
  const canRun = may(role, 'jobs.run');
  const [filters, setFilters] = useState<Filters>({});
  const active = Object.values(filters).some((v) => v !== undefined && v !== '');
  const set = (patch: Filters) => setFilters((f) => ({ ...f, ...patch }));

  const columns = templateRunColumns(t, fmt, (j) => runSource(j, schedules, triggers));
  const view = useListView('template-runs', columns, { mode: 'server', urlState: false, resetKey: JSON.stringify(filters) });
  const jobs = useJobsPage(org, { ...filters, template: templateId }, view.serverQuery, { enabled: view.ready });

  function rowActions(job: Job): RowAction[] {
    const items: RowAction[] = [{ id: 'open', label: t.common.open, onSelect: () => void navigate({ to: '/o/$org/runs/$id', params: { org, id: job.id } }) }];
    if (canRun && isCancellable(job.status)) {
      items.push({ id: 'cancel', label: t.runs.list.cancelItem, onSelect: () => void cancel.cancel(job.id), disabled: cancel.isPending(job.id) });
    }
    return items;
  }

  return (
    <>
      <p className="text-[13px] text-muted-foreground">{r.hint}</p>
      {jobs.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {jobs.error instanceof Error ? jobs.error.message : r.readFailed}
        </p>
      ) : (
        <DataList
          label={r.label}
          columns={columns}
          rows={jobs.data?.jobs ?? []}
          rowKey={(j) => j.id}
          rowLink={(j) => ({ to: '/o/$org/runs/$id', params: { org, id: j.id }, label: shortId(j.id) })}
          rowActions={rowActions}
          loading={!view.ready || jobs.isPending || jobs.isPlaceholderData}
          empty={active ? r.noMatch : r.empty}
          toolbar={
            <ListToolbar>
              <FilterSelect
                label={t.runs.list.filterStatus}
                allLabel={t.runs.list.allStatuses}
                options={[{ value: 'active', label: t.runs.list.statusActive }, ...RUN_STATUSES.map((s) => ({ value: s, label: statusLabel(s, t) }))]}
                value={filters.status}
                onChange={(status) => set({ status })}
              />
              <FilterSelect
                label={t.runs.list.filterTrigger}
                allLabel={t.runs.list.allTriggers}
                options={TRIGGERS.map((k) => ({ value: k, label: triggerLabel(k, t) }))}
                value={filters.trigger}
                onChange={(trigger) => set({ trigger })}
              />
              <RunModeFilter value={filters.checkMode} onChange={(checkMode) => set({ checkMode })} />
              {active && <ResetFilters onReset={() => setFilters({})} />}
            </ListToolbar>
          }
          {...view.listProps(jobs.isPlaceholderData ? undefined : jobs.data?.total, t.runs.list.count)}
        />
      )}
    </>
  );
}

/**
 * templateRunColumns is every column of a template's run history (list id
 * `template-runs`). The server sorts by created (the default, newest
 * first), trigger and status; "Started" is the run's created_at, when it was
 * asked for. A row's cells hold no control: the whole row is its link.
 */
function templateRunColumns(t: Messages, fmt: Pick<Format, 'relative' | 'dateTime'>, source: (j: Job) => string | undefined): DataListColumn<Job>[] {
  const r = t.templates.page.runs;
  const c = r.columns;
  return [
    {
      id: 'run',
      label: c.run,
      width: '90px',
      required: true,
      defaultVisible: true,
      render: (j) => <span className="font-mono">{shortId(j.id)}</span>,
    },
    {
      id: 'started',
      label: c.started,
      width: '130px',
      sortKey: 'created',
      defaultVisible: true,
      render: (j) => (
        <span className={`text-muted-foreground ${CELL_MINOR}`} title={fmt.dateTime(j.created_at)}>
          {fmt.relative(j.created_at)}
        </span>
      ),
    },
    {
      id: 'by',
      label: c.by,
      width: 'minmax(0,1fr)',
      sortKey: 'trigger',
      defaultVisible: true,
      render: (j) => {
        const name = source(j);
        const trigger = triggerLabel(j.trigger, t);
        return (
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="truncate">{name ? r.bySource(trigger, name) : trigger}</span>
            {j.kind !== 'run' && <span className={`truncate text-muted-foreground ${CELL_MINOR}`}>{kindLabel(j.kind, t)}</span>}
          </span>
        );
      },
    },
    {
      id: 'commit',
      label: c.commit,
      width: '120px',
      defaultVisible: true,
      render: (j) =>
        j.pinned_commit ? (
          <span className={`font-mono ${CELL_MINOR}`} title={j.pinned_commit}>
            {j.pinned_commit.slice(0, 12)}
          </span>
        ) : (
          <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>
        ),
    },
    {
      id: 'hosts',
      label: c.hosts,
      width: '90px',
      defaultVisible: true,
      render: (j) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{j.host_count ? r.hosts(j.host_count) : '—'}</span>,
    },
    {
      id: 'result',
      label: c.result,
      width: '150px',
      sortKey: 'status',
      defaultVisible: true,
      // A dry run is a run in check mode: its badge sits next to the result.
      render: (j) => (
        <span className="flex min-w-0 items-center gap-1.5">
          <Pill tone={statusTone(j.status, j.withdrawn_at_claim)}>{statusLabel(j.status, t, j.withdrawn_at_claim)}</Pill>
          {j.check_mode && <Pill tone="accent">{t.runs.dryRun.badge}</Pill>}
        </span>
      ),
    },
  ];
}
