import { useMemo } from 'react';
import { useParams } from '@tanstack/react-router';
import { useOrgActivityPage } from '../../api/hooks/useActivity.ts';
import { useMembers } from '../../api/hooks/useMembers.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ACTIVITY_FILTERS, SINCE_RANGES, sinceOf } from './activitySearch.ts';
import { activityColumns } from './activityColumns.tsx';
import { ObjectTypeChips } from './ObjectTypeChips.tsx';
import { PageHeader } from '../../ui/settings.tsx';

/**
 * ActivityPage follows design/mockups/build.mjs's ACTIVITY FEED artboard's
 * object-type filter chips (the feed itself is a paged table, not the
 * mockup's day groups). The mockup's right-
 * hand "Event detail" and "Actors" panels depend on data (a before/after
 * diff, per-actor totals) the audit API does not expose — see
 * task-15a-report.md — so this page keeps the feed itself and the filter
 * bar, which the API does support in full (actor, object_type, since).
 *
 * Two fixes from that report's fix round 1: the feed used to show
 * `actor_kind · <uuid prefix>` for every event, and there was no actor
 * filter even though the API supports one (finding "Important 6"). Actor
 * names are now resolved against the members list this page already
 * loads — matching this org's own shape, since the API's `actor` filter
 * is a member's user id, not free text.
 *
 * The feed is a list (id `activity`): the server pages it and sorts it by
 * time. The actor, the object type and the start of the range are in the URL
 * (activitySearch.ts); a change starts again at page 1.
 */
export function ActivityPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const { filters, active, setFilter, reset } = useListFilters(ACTIVITY_FILTERS);
  const objectType = filters.type ?? '';
  const actorId = filters.actor ?? '';
  const since = useMemo(() => sinceOf(filters.since), [filters.since]);

  const members = useMembers(org);
  const memberNames = new Map((members.data?.members ?? []).map((m) => [m.user_id, m.display_name]));

  const columns = activityColumns({ t, fmt, memberNames });
  const view = useListView('activity', columns, { mode: 'server', resetKey: JSON.stringify(filters) });
  const activity = useOrgActivityPage(org, { objectType, actor: actorId, since }, view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(activity);
  if (guard) return guard;

  return (
    <>
      <PageHeader title={t.activity.title} description={t.activity.subtitle} />

      <DataList
        label={t.activity.list.label}
        columns={columns}
        rows={activity.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={activity.isPending || activity.isPlaceholderData}
        empty={active ? t.activity.noMatch : t.activity.empty}
        toolbar={
          <ListToolbar>
            {/* A plain filter never offers to create — narrowing a view is not
                building one (v0.4g Move 3, same reasoning as HostsListPage's
                Filter by zone). */}
            <FilterSelect
              label={t.activity.filterByActor}
              allLabel={t.activity.anyActor}
              options={(members.data?.members ?? []).map((m) => ({ value: m.user_id, label: m.display_name }))}
              value={filters.actor}
              onChange={(v) => setFilter('actor', v)}
            />
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
        {...view.listProps(activity.isPlaceholderData ? undefined : activity.data?.total, t.activity.list.count)}
      />
    </>
  );
}
