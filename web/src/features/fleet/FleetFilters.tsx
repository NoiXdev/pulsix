import { Select } from '../../ui/Select.tsx';
import type { MonitoringGroup, Zone } from '../../api/types.ts';
import type { FleetFilter } from '../../api/hooks/useFleet.ts';
import { useT } from '../../i18n/useT.ts';

export interface FleetFiltersProps {
  zones: Zone[];
  groups: MonitoringGroup[];
  value: FleetFilter;
  onChange: (value: FleetFilter) => void;
  /** The API's own message when `value.tag` was refused (a 422 for a
   * selector expression this route does not accept) — never a
   * client-invented one, so the wording always matches whatever the
   * server actually enforces. */
  tagError?: string | null;
}

const FIELD =
  'h-[30px] rounded-md border border-border bg-card px-2.5 text-xs text-foreground outline-none placeholder:text-muted-foreground';

/**
 * FleetFilters is the fleet page's own filter row: zone, monitoring group
 * and a single tag token — the same three query parameters
 * GET /orgs/{org}/fleet accepts (fleet_handlers.go's fleetInput), no more.
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
export function FleetFilters({ zones, groups, value, onChange, tagError }: FleetFiltersProps) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* Each label is a sibling of its Select, not a wrapper around it, with
          htmlFor carrying the association instead: an <li role="option"> in
          Select's own dropdown is not interactive content, so wrapping it
          inside the label would make clicking a row trigger the label's own
          activation behaviour — a synthetic click at the input that reopens
          the list downshift just closed (its InputClick reducer toggles
          isOpen unconditionally). A native <select> was interactive content,
          so this never showed up there. */}
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="fleet-filter-zone">{t.fleet.filters.zone}</label>
        <Select
          id="fleet-filter-zone"
          options={zones.map((z) => ({ value: z.id, label: z.name }))}
          value={value.zone ?? ''}
          onChange={(v) => onChange({ ...value, zone: v || undefined })}
          placeholder={t.fleet.filters.allZones}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="fleet-filter-group">{t.fleet.filters.group}</label>
        <Select
          id="fleet-filter-group"
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
          value={value.group ?? ''}
          onChange={(v) => onChange({ ...value, group: v || undefined })}
          placeholder={t.fleet.filters.allGroups}
        />
      </div>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        {t.fleet.filters.tag}
        <input
          aria-label={t.fleet.filters.tag}
          type="text"
          className={`${FIELD} w-40 font-mono`}
          placeholder={t.fleet.filters.tagPlaceholder}
          value={value.tag ?? ''}
          onChange={(e) => onChange({ ...value, tag: e.target.value || undefined })}
        />
      </label>
      {tagError && (
        <span role="alert" className="text-[11px] text-destructive">
          {tagError}
        </span>
      )}
    </div>
  );
}
