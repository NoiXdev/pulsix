import type { AlertFilter } from '../../api/hooks/useAlerts.ts';

export interface AlertFiltersProps {
  value: AlertFilter;
  onChange: (value: AlertFilter) => void;
}

const FIELD =
  'h-[30px] rounded-md border border-border bg-card px-2.5 text-xs text-foreground outline-none placeholder:text-muted-foreground';

/** toTriBool/fromTriBool round-trip a three-way filter (any/true/false)
 * through a <select>, which has no native concept of "unset" beyond its
 * own empty-string option — the same shape FleetFilters' own zone/group
 * selects already use for "All". */
function toTriBool(v: boolean | undefined): string {
  return v === undefined ? '' : String(v);
}
function fromTriBool(v: string): boolean | undefined {
  return v === '' ? undefined : v === 'true';
}

/**
 * AlertFilters is the Alerts page's own filter row: every query parameter
 * GET /orgs/{org}/alerts accepts except `host` — that one serves the host
 * page's own embedded view (v0.4a design doc, "the host view is a filter
 * on this list, not its own endpoint"), not an operator-facing control on
 * the organization-wide page.
 */
export function AlertFilters({ value, onChange }: AlertFiltersProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        State
        <select
          aria-label="State"
          className={FIELD}
          value={value.state ?? ''}
          onChange={(e) => onChange({ ...value, state: (e.target.value || undefined) as AlertFilter['state'] })}
        >
          <option value="">All</option>
          <option value="open">Open</option>
          <option value="closed">Closed</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Severity
        <select
          aria-label="Severity"
          className={FIELD}
          value={value.severity ?? ''}
          onChange={(e) => onChange({ ...value, severity: (e.target.value || undefined) as AlertFilter['severity'] })}
        >
          <option value="">All</option>
          <option value="warn">Warn</option>
          <option value="crit">Crit</option>
          <option value="unknown">Unknown</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Scope
        <select
          aria-label="Scope"
          className={FIELD}
          value={value.scope ?? ''}
          onChange={(e) => onChange({ ...value, scope: (e.target.value || undefined) as AlertFilter['scope'] })}
        >
          <option value="">All</option>
          <option value="tuple">Tuple</option>
          <option value="host">Host</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Acknowledged
        <select
          aria-label="Acknowledged"
          className={FIELD}
          value={toTriBool(value.acknowledged)}
          onChange={(e) => onChange({ ...value, acknowledged: fromTriBool(e.target.value) })}
        >
          <option value="">Any</option>
          <option value="true">Acknowledged</option>
          <option value="false">Unacknowledged</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Muted
        <select
          aria-label="Muted"
          className={FIELD}
          value={toTriBool(value.muted)}
          onChange={(e) => onChange({ ...value, muted: fromTriBool(e.target.value) })}
        >
          <option value="">Any</option>
          <option value="true">Muted</option>
          <option value="false">Not muted</option>
        </select>
      </label>
    </div>
  );
}
