import { useT } from '../../i18n/useT.ts';
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
        <label htmlFor="alert-filter-state">{t.alerts.filters.state}</label>
        <Select
          id="alert-filter-state"
          options={[
            { value: 'open', label: t.alerts.filters.open },
            { value: 'closed', label: t.alerts.filters.closed },
          ]}
          value={value.state ?? ''}
          onChange={(v) => onChange({ ...value, state: (v || undefined) as AlertFilter['state'] })}
          placeholder={t.alerts.filters.all}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-severity">{t.alerts.filters.severity}</label>
        <Select
          id="alert-filter-severity"
          options={[
            { value: 'warn', label: t.alerts.filters.warn },
            { value: 'crit', label: t.alerts.filters.crit },
            { value: 'unknown', label: t.alerts.filters.unknown },
          ]}
          value={value.severity ?? ''}
          onChange={(v) => onChange({ ...value, severity: (v || undefined) as AlertFilter['severity'] })}
          placeholder={t.alerts.filters.all}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-scope">{t.alerts.filters.scope}</label>
        <Select
          id="alert-filter-scope"
          options={[
            { value: 'tuple', label: t.alerts.filters.tuple },
            { value: 'host', label: t.alerts.filters.host },
          ]}
          value={value.scope ?? ''}
          onChange={(v) => onChange({ ...value, scope: (v || undefined) as AlertFilter['scope'] })}
          placeholder={t.alerts.filters.all}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-acknowledged">{t.alerts.filters.acknowledged}</label>
        <Select
          id="alert-filter-acknowledged"
          options={[
            { value: 'true', label: t.alerts.filters.acknowledged },
            { value: 'false', label: t.alerts.filters.unacknowledged },
          ]}
          value={toTriBool(value.acknowledged)}
          onChange={(v) => onChange({ ...value, acknowledged: fromTriBool(v) })}
          placeholder={t.alerts.filters.any}
        />
      </div>
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <label htmlFor="alert-filter-muted">{t.alerts.filters.muted}</label>
        <Select
          id="alert-filter-muted"
          options={[
            { value: 'true', label: t.alerts.filters.muted },
            { value: 'false', label: t.alerts.filters.notMuted },
          ]}
          value={toTriBool(value.muted)}
          onChange={(v) => onChange({ ...value, muted: fromTriBool(v) })}
          placeholder={t.alerts.filters.any}
        />
      </div>
    </div>
  );
}
