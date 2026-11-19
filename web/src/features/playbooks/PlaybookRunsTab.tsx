import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { useJobsPage } from '../../api/hooks/useJobs.ts';
import type { Job, Template } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListView } from '../lists/useListView.ts';
import { runColumns } from '../runs/runColumns.tsx';
import { RUN_STATUSES } from '../runs/runsSearch.ts';
import { isCancellable, shortId, statusLabel } from '../runs/status.ts';
import { TRIGGERS, triggerLabel } from '../runs/trigger.ts';
import { useCancelRuns } from '../runs/useCancelRun.ts';

interface Filters {
  status?: string;
  trigger?: string;
  template?: string;
}

/**
 * PlaybookRunsTab is the playbook page's Runs tab: the runs list (its
 * columns, row menu and server-side sorting and paging) narrowed to this
 * playbook by the server (`playbook_id`: the runs of the templates that use
 * it). Status, trigger and template narrow it further. The page's URL holds
 * only the tab, so sort, page and filters live in the component (list id
 * `playbook-runs`).
 */
export function PlaybookRunsTab({ org, playbookId, templates, canRun }: { org: string; playbookId: string; templates: readonly Template[]; canRun: boolean }) {
  const t = useT();
  const fmt = useFormat();
  const navigate = useNavigate();
  const cancel = useCancelRuns(org);
  const [filters, setFilters] = useState<Filters>({});
  const active = Object.values(filters).some(Boolean);
  const set = (patch: Filters) => setFilters((f) => ({ ...f, ...patch }));
  const filter = { ...filters, playbook: playbookId };

  const columns = runColumns(t, fmt);
  const view = useListView('playbook-runs', columns, { mode: 'server', urlState: false, resetKey: JSON.stringify(filters) });
  const jobs = useJobsPage(org, filter, view.serverQuery, { enabled: view.ready });

  function rowActions(job: Job): RowAction[] {
    const items: RowAction[] = [{ id: 'open', label: t.common.open, onSelect: () => void navigate({ to: '/o/$org/runs/$id', params: { org, id: job.id } }) }];
    if (canRun && isCancellable(job.status)) {
      items.push({ id: 'cancel', label: t.runs.list.cancelItem, onSelect: () => void cancel.cancel(job.id), disabled: cancel.isPending(job.id) });
    }
    return items;
  }

  return (
    <>
      <p className="text-[13px] text-muted-foreground">{t.playbooks.page.runs.hint}</p>
      {jobs.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {jobs.error instanceof Error ? jobs.error.message : t.playbooks.page.latest.readFailed}
        </p>
      ) : (
        <DataList
          label={t.playbooks.page.runs.label}
          columns={columns}
          rows={jobs.data?.jobs ?? []}
          rowKey={(j) => j.id}
          rowLink={(j) => ({ to: '/o/$org/runs/$id', params: { org, id: j.id }, label: shortId(j.id) })}
          rowActions={rowActions}
          loading={!view.ready || jobs.isPending || jobs.isPlaceholderData}
          empty={active ? t.playbooks.page.runs.noMatch : t.playbooks.page.runs.empty}
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
              {templates.length > 1 && (
                <FilterSelect
                  label={t.runs.list.filterTemplate}
                  allLabel={t.runs.list.allTemplates}
                  options={templates.map((tpl) => ({ value: tpl.id, label: tpl.name }))}
                  value={filters.template}
                  onChange={(template) => set({ template })}
                />
              )}
              {active && <ResetFilters onReset={() => setFilters({})} />}
            </ListToolbar>
          }
          {...view.listProps(jobs.isPlaceholderData ? undefined : jobs.data?.total, t.runs.list.count)}
        />
      )}
    </>
  );
}
