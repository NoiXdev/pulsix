import { useParams } from '@tanstack/react-router';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useRunRequests } from '../../api/hooks/useRunRequests.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { runRequestColumns } from './runRequestColumns.tsx';
import { REQUEST_KINDS, REQUEST_STATUSES, RUN_REQUESTS_FILTERS, filterRunRequests } from './runRequestsSearch.ts';
import { sortRequests, useNow } from './runRequestStatus.ts';
import { shortId } from './status.ts';

/**
 * RunRequestsPage lists the runs an assistant asked for (spec 2026-11-16,
 * "Run requests"; list id `run-requests`): the pending
 * ones first, soonest to expire on top with a countdown, then the decided
 * and expired ones with their status. The whole list is one request, sorted
 * and paged in the browser (until a column is chosen, in that order). Each
 * row opens the request, where a person with jobs.run approves or rejects
 * it.
 */
export function RunRequestsPage() {
  const t = useT();
  const r = t.runs.requests;
  const fmt = useFormat();
  const now = useNow();
  const { org } = useParams({ strict: false }) as { org: string };
  const requests = useRunRequests(org);
  const templates = useTemplates(org);

  const templateNames = new Map((templates.data?.templates ?? []).map((tpl) => [tpl.id, tpl.name]));
  const columns = runRequestColumns({
    t,
    templateName: (id) => templateNames.get(id) ?? shortId(id),
    dateTime: (iso) => fmt.dateTime(iso),
    relative: (iso) => fmt.relative(iso, now),
  });
  const { filters, active, setFilter, reset } = useListFilters(RUN_REQUESTS_FILTERS);
  const view = useListView('run-requests', columns, { mode: 'client', rowKey: (q) => q.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(requests);
  if (guard) return guard;
  if (!requests.data) return <QueryLoading />;

  const all = sortRequests(requests.data.requests ?? []);
  const pending = all.filter((q) => q.status === 'pending').length;
  const shown = filterRunRequests(all, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = r.filters;
  // Only templates a request asked for are offered; one only the URL names stays so it can be cleared.
  const templateIds = [...new Set([...all.map((q) => q.template_id), ...(filters.template ? [filters.template] : [])])];
  const templateOptions = templateIds.map((id) => ({ value: id, label: templateNames.get(id) ?? shortId(id) })).sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">{r.title}</h1>
        <span className="text-[13px] text-muted-foreground">{r.summary}</span>
        <span className="text-[13px] text-muted-foreground">{r.pendingCount(pending)}</span>
      </div>
      <DataList
        label={r.label}
        columns={columns}
        rows={rows}
        rowKey={(q) => q.id}
        rowLink={(q) => ({ to: '/o/$org/runs/requests/$id', params: { org, id: q.id }, label: q.reason || shortId(q.id) })}
        loading={!view.ready}
        empty={active ? r.noMatch : r.empty}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={all.length} noun={r.count} /> : undefined}>
            <FilterSelect
              label={f.status}
              allLabel={f.allStatuses}
              options={REQUEST_STATUSES.map((v) => ({ value: v, label: r.status[v] }))}
              value={filters.status}
              onChange={(v) => setFilter('status', v as typeof filters.status)}
            />
            <FilterSelect
              label={f.kind}
              allLabel={f.allKinds}
              options={REQUEST_KINDS.map((v) => ({ value: v, label: r.kind[v] }))}
              value={filters.kind}
              onChange={(v) => setFilter('kind', v as typeof filters.kind)}
            />
            <FilterSelect label={f.template} allLabel={f.allTemplates} options={templateOptions} value={filters.template} onChange={(v) => setFilter('template', v)} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, r.count)}
      />
    </>
  );
}
