import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Clock, Pause } from 'lucide-react';
import { DataList } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { ActiveFilter, FilterMenu, ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { usePauseAllSchedules, useSchedules } from '../../api/hooks/useSchedules.ts';
import { may } from '../../lib/roles.ts';
import { ScheduleCalendar } from '../calendar/ScheduleCalendar.tsx';
import { ScheduleEditor } from './ScheduleEditor.tsx';
import { scheduleColumns } from './scheduleColumns.tsx';
import { useScheduleActions } from './useScheduleActions.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { SCHEDULE_STATUSES, SCHEDULES_FILTERS, filterSchedules } from './schedulesSearch.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

/** SchedulesPage is the Scheduler artboard (spec 2026-09-18 §2): every
 * schedule of the organization with status, template, timetable, next run
 * and last result; "Pause all"; "New schedule"; the detail panel for the
 * selected one (?id=), optionally filtered to one template (?template=).
 * The list (id `schedules`) is one request, sorted and paged in the browser;
 * Pause or Resume, Edit and Delete are the row menu's. */
export function SchedulesPage() {
  const t = useT();
  const m = t.schedules;
  const { org } = useParams({ strict: false }) as { org: string };
  const search = useSearch({ strict: false }) as { id?: string };
  const { filters, active, setFilter, reset } = useListFilters(SCHEDULES_FILTERS);
  const navigate = useNavigate();
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'templates.write');
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const schedules = useSchedules(org, filters.template);
  const templates = useTemplates(org);
  const pauseAll = usePauseAllSchedules(org);

  const select = (id?: string) => void navigate({ to: '/o/$org/schedules', params: { org }, search: (prev) => ({ ...prev, id }) });
  const rowActions = useScheduleActions(org, {
    canWrite,
    edit: (id) => select(id),
    deleted: (id) => {
      if (search.id === id) select(undefined);
    },
  });
  const templateList = templates.data?.templates ?? [];
  const templateName = (id: string) => templateList.find((tpl) => tpl.id === id)?.name ?? id.slice(0, 8);
  const now = new Date();
  const columns = scheduleColumns({ t, templateName, now });
  const view = useListView('schedules', columns, { mode: 'client', resetKey: JSON.stringify(filters), rowKey: (s) => s.id });

  const guard = useQueryGuard(schedules);
  if (guard) return guard;
  if (!schedules.data) return <QueryLoading />;
  const list = schedules.data.schedules ?? [];
  const shown = filterSchedules(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = m.filters;

  async function onPauseAll() {
    if (!(await ask({ message: m.pauseAllConfirm, confirmLabel: m.pauseAll }))) return;
    try {
      const res = await pauseAll.mutateAsync();
      toast.show(m.pausedCount(res.paused));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.pauseFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{m.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {m.summary(list.length, list.filter((s) => !s.paused).length)}
          </span>
        </div>
        {canWrite && (
          <div className="flex gap-2">
            <Button type="button" onClick={() => void onPauseAll()}>
              <Pause size={14} /> {m.pauseAll}
            </Button>
            <Button type="button" variant="primary" onClick={() => select('new')}>
              <Clock size={14} /> {m.newSchedule}
            </Button>
          </div>
        )}
      </div>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_420px]">
        <DataList
          label={m.label}
          columns={columns}
          rows={rows}
          rowKey={(sc) => sc.id}
          rowLink={(sc) => ({ to: '/o/$org/schedules', params: { org }, search: { sort: view.sort?.key, order: view.sort?.order, page: view.page > 1 ? view.page : undefined, status: filters.status, template: filters.template, id: sc.id }, label: sc.name })}
          rowActions={rowActions}
          loading={!view.ready}
          empty={active ? m.noMatch : m.empty}
          toolbar={
            <ListToolbar count={active ? <ResultCount shown={shown.length} total={list.length} noun={m.count} /> : undefined}>
              <FilterSelect
                label={f.status}
                allLabel={f.anyStatus}
                options={SCHEDULE_STATUSES.map((v) => ({ value: v, label: v === 'paused' ? m.paused : m.active }))}
                value={filters.status}
                onChange={(v) => setFilter('status', v as typeof filters.status)}
              />
              {!filters.template && (
                <FilterMenu
                  items={templateList.map((tpl) => ({ id: tpl.id, label: m.template(tpl.name), onSelect: () => setFilter('template', tpl.id) }))}
                />
              )}
              {filters.template && <ActiveFilter label={m.template(templateName(filters.template))} onRemove={() => setFilter('template', undefined)} />}
              {active && <ResetFilters onReset={reset} />}
            </ListToolbar>
          }
          {...view.listProps(total, m.count)}
        />
        <div className="lg:col-start-1">
          <ScheduleCalendar org={org} />
        </div>
        {search.id && (
          <ScheduleEditor
            key={search.id}
            org={org}
            id={search.id}
            templates={templateList}
            defaultTemplate={filters.template}
            canWrite={canWrite}
            canRun={canRun}
            onClose={() => select(undefined)}
          />
        )}
      </div>
    </>
  );
}
