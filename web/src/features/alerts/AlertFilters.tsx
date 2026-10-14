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
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        State
        <Select
          aria-label="State"
          options={[
            { value: 'open', label: 'Open' },
            { value: 'closed', label: 'Closed' },
          ]}
          value={value.state ?? ''}
          onChange={(v) => onChange({ ...value, state: (v || undefined) as AlertFilter['state'] })}
          placeholder="All"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Severity
        <Select
          aria-label="Severity"
          options={[
            { value: 'warn', label: 'Warn' },
            { value: 'crit', label: 'Crit' },
            { value: 'unknown', label: 'Unknown' },
          ]}
          value={value.severity ?? ''}
          onChange={(v) => onChange({ ...value, severity: (v || undefined) as AlertFilter['severity'] })}
          placeholder="All"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Scope
        <Select
          aria-label="Scope"
          options={[
            { value: 'tuple', label: 'Tuple' },
            { value: 'host', label: 'Host' },
          ]}
          value={value.scope ?? ''}
          onChange={(v) => onChange({ ...value, scope: (v || undefined) as AlertFilter['scope'] })}
          placeholder="All"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Acknowledged
        <Select
          aria-label="Acknowledged"
          options={[
            { value: 'true', label: 'Acknowledged' },
            { value: 'false', label: 'Unacknowledged' },
          ]}
          value={toTriBool(value.acknowledged)}
          onChange={(v) => onChange({ ...value, acknowledged: fromTriBool(v) })}
          placeholder="Any"
        />
      </label>
      <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
        Muted
        <Select
          aria-label="Muted"
          options={[
            { value: 'true', label: 'Muted' },
            { value: 'false', label: 'Not muted' },
          ]}
          value={toTriBool(value.muted)}
          onChange={(v) => onChange({ ...value, muted: fromTriBool(v) })}
          placeholder="Any"
        />
      </label>
    </div>
  );
}
