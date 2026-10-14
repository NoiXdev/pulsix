import { Select } from '../../ui/Select.tsx';
import type { AlertFilter } from '../../api/hooks/useAlerts.ts';

export interface AlertFiltersProps {
  value: AlertFilter;
  onChange: (value: AlertFilter) => void;
}

/** toTriBool/fromTriBool round-trip a three-way filter (any/true/false)
 * through a Select, which has no native concept of "unset" beyond its own
 * empty-string placeholder row — the same shape FleetFilters' own
 * zone/group pickers already use for "All". */
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
      {/* Each label is a sibling of its Select, not a wrapper around it, with
          htmlFor carrying the association instead: an <li role="option"> in
          Select's own dropdown is not interactive content, so wrapping it
          inside the label would make clicking a row trigger the label's own
          activation behaviour — a synthetic click at the input that reopens
          the list downshift just closed (its InputClick reducer toggles
          isOpen unconditionally). A native <select> was interactive content,
          so this never showed up there. */}
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-state">State</label>
        <Select
          id="alert-filter-state"
          options={[
            { value: 'open', label: 'Open' },
            { value: 'closed', label: 'Closed' },
          ]}
          value={value.state ?? ''}
          onChange={(v) => onChange({ ...value, state: (v || undefined) as AlertFilter['state'] })}
          placeholder="All"
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-severity">Severity</label>
        <Select
          id="alert-filter-severity"
          options={[
            { value: 'warn', label: 'Warn' },
            { value: 'crit', label: 'Crit' },
            { value: 'unknown', label: 'Unknown' },
          ]}
          value={value.severity ?? ''}
          onChange={(v) => onChange({ ...value, severity: (v || undefined) as AlertFilter['severity'] })}
          placeholder="All"
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-scope">Scope</label>
        <Select
          id="alert-filter-scope"
          options={[
            { value: 'tuple', label: 'Tuple' },
            { value: 'host', label: 'Host' },
          ]}
          value={value.scope ?? ''}
          onChange={(v) => onChange({ ...value, scope: (v || undefined) as AlertFilter['scope'] })}
          placeholder="All"
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-acknowledged">Acknowledged</label>
        <Select
          id="alert-filter-acknowledged"
          options={[
            { value: 'true', label: 'Acknowledged' },
            { value: 'false', label: 'Unacknowledged' },
          ]}
          value={toTriBool(value.acknowledged)}
          onChange={(v) => onChange({ ...value, acknowledged: fromTriBool(v) })}
          placeholder="Any"
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-muted">Muted</label>
        <Select
          id="alert-filter-muted"
          options={[
            { value: 'true', label: 'Muted' },
            { value: 'false', label: 'Not muted' },
          ]}
          value={toTriBool(value.muted)}
          onChange={(v) => onChange({ ...value, muted: fromTriBool(v) })}
          placeholder="Any"
        />
      </div>
    </div>
  );
}
