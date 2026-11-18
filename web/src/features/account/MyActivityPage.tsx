import { useMemo } from 'react';
import { CELL_MINOR, DataList, type DataListColumn } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { QueryError } from '../../ui/QueryState.tsx';
import { useMyActivityPage } from '../../api/hooks/useActivity.ts';
import { useOrgs } from '../../api/hooks/useOrgs.ts';
import type { Event } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { ObjectTypeChips } from '../activity/ObjectTypeChips.tsx';
import { known } from '../activity/objectTypes.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { MY_ACTIVITY_FILTERS, SINCE_RANGES, sinceOf } from '../activity/activitySearch.ts';
import { PageHeader } from '../../ui/settings.tsx';

/**
 * MyActivityPage is Settings > My activity: what the signed-in person did,
 * in every organization they belong to (GET /me/activity). The server
 * pages and sorts it by time; the object-type chips and the start of the
 * range filter it like the organization feed's, in the URL. The API has no
 * organization filter here, so the list offers none.
 */
export function MyActivityPage() {
  const t = useT();
  const fmt = useFormat();
  const orgs = useOrgs();
  const { filters, active, setFilter, reset } = useListFilters(MY_ACTIVITY_FILTERS);
  const objectType = filters.type ?? '';
  const since = useMemo(() => sinceOf(filters.since), [filters.since]);
  const orgNames = new Map((orgs.data?.orgs ?? []).map((o) => [o.id, o.name]));

  const columns: DataListColumn<Event>[] = [
    {
      id: 'at',
      label: t.activity.my.at,
      width: '170px',
      sortKey: 'at',
      required: true,
      defaultVisible: true,
      render: (e) => <span className={`font-mono text-muted-foreground ${CELL_MINOR}`}>{fmt.dateTime(e.at, { dateStyle: 'medium', timeStyle: 'short' })}</span>,
    },
    {
      id: 'organization',
      label: t.activity.my.organization,
      width: '160px',
      defaultVisible: true,
      render: (e) => <span className="truncate">{(e.org_id && orgNames.get(e.org_id)) || '—'}</span>,
    },
    {
      id: 'action',
      label: t.activity.my.action,
      width: 'minmax(0,1fr)',
      defaultVisible: true,
      render: (e) => <span className="truncate">{e.summary}</span>,
    },
    {
      id: 'object',
      label: t.activity.my.object,
      width: '130px',
      defaultVisible: true,
      render: (e) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{known(t.activity.objectTypes, e.object_type)}</span>,
    },
  ];

  const view = useListView('my-activity', columns, { mode: 'server', resetKey: JSON.stringify(filters) });
  const q = useMyActivityPage({ objectType, since }, view.serverQuery, { enabled: view.ready });

  if (q.isError && !q.data) return <QueryError error={q.error} />;

  return (
    <>
      <PageHeader title={t.activity.my.title} description={t.activity.my.subtitle} />
      <DataList
        label={t.activity.my.label}
        columns={columns}
        rows={q.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={q.isPending || q.isPlaceholderData}
        empty={active ? t.activity.noMatch : t.activity.my.empty}
        toolbar={
          <ListToolbar>
            <FilterSelect
              label={t.activity.filterBySince}
              allLabel={t.activity.anyTime}
              options={SINCE_RANGES.map((r) => ({ value: r, label: t.activity.since[r] }))}
              value={filters.since}
              onChange={(v) => setFilter('since', v as typeof filters.since)}
            />
            <ObjectTypeChips value={objectType} onChange={(v) => setFilter('type', (v || undefined) as typeof filters.type)} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(q.isPlaceholderData ? undefined : q.data?.total, t.activity.my.count)}
      />
    </>
  );
}
