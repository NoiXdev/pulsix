import { Select } from '../../ui/Select.tsx';
import type { MonitoringGroup, Zone } from '../../api/types.ts';
import type { FleetFilter } from '../../api/hooks/useFleet.ts';

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
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Zone
        <Select
          aria-label="Zone"
          options={zones.map((z) => ({ value: z.id, label: z.name }))}
          value={value.zone ?? ''}
          onChange={(v) => onChange({ ...value, zone: v || undefined })}
          placeholder="All zones"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Monitoring group
        <Select
          aria-label="Monitoring group"
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
          value={value.group ?? ''}
          onChange={(v) => onChange({ ...value, group: v || undefined })}
          placeholder="All groups"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Tag
        <input
          aria-label="Tag"
          type="text"
          className={`${FIELD} w-40 font-mono`}
          placeholder="key or key=value"
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
