import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import type { MonitoringGroup, Zone } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import type { ListFilters } from '../lists/filterSearch.ts';
import type { FLEET_FILTERS } from './fleetSearch.ts';

export interface FleetFiltersProps {
  zones: Zone[];
  groups: MonitoringGroup[];
  /** The page's useListFilters(FLEET_FILTERS): the filters live in the URL. */
  list: ListFilters<typeof FLEET_FILTERS>;
  /** The API's own message when the tag was refused (a 422 for a
   * selector expression this route does not accept) — never a
   * client-invented one, so the wording always matches whatever the
   * server actually enforces. */
  tagError?: string | null;
  /** The rows the filters leave and the rows the server answered with. */
  shown?: number;
  total?: number;
}

/**
 * FleetFilters is the fleet page's filter bar: a host search and a state
 * picker (both narrow the answer in the browser), then zone, monitoring
 * group and a single tag token, the three query parameters GET
 * /orgs/{org}/fleet accepts (fleet_handlers.go's fleetInput). All of them
 * are in the URL (fleetSearch.ts).
 *
 * The tag field is deliberately a plain text input, not a selector
 * builder: the route's own Tag filter implements only equality and
 * existence (FleetFilter.Tag, internal/rules/states.go) and refuses "!=",
 * "in(...)" or boolean composition with a 422 — offering a control that
 * invites any of those would just relocate the refusal from "typing it"
 * to "clicking it together", so the caller (FleetPage) is left to surface
 * the API's own message via `tagError` instead of this component guessing
 * at validation rules that belong to the server.
 */
export function FleetFilters({ zones, groups, list, tagError, shown, total }: FleetFiltersProps) {
  const t = useT();
  const f = t.fleet.filters;
  const { filters, setFilter } = list;
  return (
    <ListToolbar count={list.active && shown !== undefined ? <ResultCount shown={shown} total={total} noun={t.fleet.count} /> : undefined}>
      <SearchInput label={f.search} placeholder={f.searchPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
      <FilterSelect
        label={f.state}
        allLabel={f.anyState}
        options={(['crit', 'warn', 'ok', 'unknown'] as const).map((s) => ({ value: s, label: t.fleet.state[s] }))}
        value={filters.state}
        onChange={(v) => setFilter('state', v as typeof filters.state)}
      />
      <FilterSelect
        label={f.zone}
        allLabel={f.allZones}
        options={zones.map((z) => ({ value: z.id, label: z.name }))}
        value={filters.zone}
        onChange={(v) => setFilter('zone', v)}
      />
      <FilterSelect
        label={f.group}
        allLabel={f.allGroups}
        options={groups.map((g) => ({ value: g.id, label: g.name }))}
        value={filters.group}
        onChange={(v) => setFilter('group', v)}
      />
      <SearchInput label={f.tag} placeholder={f.tagPlaceholder} value={filters.tag ?? ''} onChange={(tag) => setFilter('tag', tag || undefined, { replace: true })} />
      {tagError && (
        <span role="alert" className="text-[11px] text-destructive">
          {tagError}
        </span>
      )}
      {list.active && <ResetFilters onReset={list.reset} />}
    </ListToolbar>
  );
}
