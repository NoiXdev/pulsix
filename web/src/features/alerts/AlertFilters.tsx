import { useT } from '../../i18n/useT.ts';
import { ActiveFilter, FilterMenu, ListToolbar, ResetFilters, type FilterMenuItem } from '../../ui/list-toolbar.tsx';
import { Select } from '../../ui/Select.tsx';
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
      {/* Each label is a sibling of its Select, not a wrapper around it, with
          htmlFor carrying the association instead: an <li role="option"> in
          Select's own dropdown is not interactive content, so wrapping it
          inside the label would make clicking a row trigger the label's own
          activation behaviour — a synthetic click at the input that reopens
          the list downshift just closed (its InputClick reducer toggles
          isOpen unconditionally). */}
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-state">{f.state}</label>
        <Select
          id="alert-filter-state"
          options={[
            { value: 'open', label: f.open },
            { value: 'closed', label: f.closed },
          ]}
          value={filters.state ?? ''}
          onChange={(v) => setFilter('state', (v || undefined) as typeof filters.state)}
          placeholder={f.all}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-severity">{f.severity}</label>
        <Select
          id="alert-filter-severity"
          options={[
            { value: 'warn', label: f.warn },
            { value: 'crit', label: f.crit },
            { value: 'unknown', label: f.unknown },
          ]}
          value={filters.severity ?? ''}
          onChange={(v) => setFilter('severity', (v || undefined) as typeof filters.severity)}
          placeholder={f.all}
        />
      </div>
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
