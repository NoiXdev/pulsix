import { useT } from '../../i18n/useT.ts';
import { ActiveFilter, FilterMenu, ListToolbar, ResetFilters, type FilterMenuItem } from '../../ui/list-toolbar.tsx';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import type { ListFilters } from '../lists/filterSearch.ts';
import { HostFilterChip } from '../runs/HostFilterChip.tsx';
import type { ALERTS_FILTERS } from './alertsSearch.ts';

export interface AlertFiltersProps {
  org: string;
  /** The page's useListFilters(ALERTS_FILTERS): the filters live in the URL. */
  list: ListFilters<typeof ALERTS_FILTERS>;
}

/**
 * AlertFilters is the Alerts page's filter bar: State and Severity as
 * pickers, the rarer Scope, Acknowledged and Muted in the "+ Filter" menu
 * and, once set, as removable pills, beside the host pill of
 * HostAlertsCard's `?host=` link. Every filter is in the URL (alertsSearch.ts).
 */
export function AlertFilters({ org, list }: AlertFiltersProps) {
  const t = useT();
  const f = t.alerts.filters;
  const { filters, setFilter } = list;

  const scopeLabel = (v: 'tuple' | 'host') => `${f.scope}: ${v === 'tuple' ? f.tuple : f.host}`;
  const ackLabel = (v: boolean) => (v ? f.acknowledged : f.unacknowledged);
  const mutedLabel = (v: boolean) => (v ? f.muted : f.notMuted);

  const menu: FilterMenuItem[] = [];
  if (filters.scope === undefined) {
    for (const v of ['tuple', 'host'] as const) menu.push({ id: `scope-${v}`, label: scopeLabel(v), onSelect: () => setFilter('scope', v) });
  }
  if (filters.acknowledged === undefined) {
    for (const v of [true, false]) menu.push({ id: `ack-${v}`, label: ackLabel(v), onSelect: () => setFilter('acknowledged', v) });
  }
  if (filters.muted === undefined) {
    for (const v of [true, false]) menu.push({ id: `muted-${v}`, label: mutedLabel(v), onSelect: () => setFilter('muted', v) });
  }

  return (
    <ListToolbar>
      <FilterSelect
        label={f.state}
        allLabel={f.anyState}
        options={[
          { value: 'open', label: f.open },
          { value: 'closed', label: f.closed },
        ]}
        value={filters.state}
        onChange={(v) => setFilter('state', v as typeof filters.state)}
      />
      <FilterSelect
        label={f.severity}
        allLabel={f.anySeverity}
        options={[
          { value: 'warn', label: f.warn },
          { value: 'crit', label: f.crit },
          { value: 'unknown', label: f.unknown },
        ]}
        value={filters.severity}
        onChange={(v) => setFilter('severity', v as typeof filters.severity)}
      />
      <FilterMenu items={menu} />
      {filters.host && <HostFilterChip org={org} hostId={filters.host} onRemove={() => setFilter('host', undefined)} />}
      {filters.scope && <ActiveFilter label={scopeLabel(filters.scope)} onRemove={() => setFilter('scope', undefined)} />}
      {filters.acknowledged !== undefined && (
        <ActiveFilter label={ackLabel(filters.acknowledged)} onRemove={() => setFilter('acknowledged', undefined)} />
      )}
      {filters.muted !== undefined && <ActiveFilter label={mutedLabel(filters.muted)} onRemove={() => setFilter('muted', undefined)} />}
      {list.active && <ResetFilters onReset={list.reset} />}
    </ListToolbar>
  );
}
