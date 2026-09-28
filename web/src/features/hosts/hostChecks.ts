import type { HostCheck, HostCheckValue } from '../../api/types.ts';

/**
 * The pure reading rules behind HostChecksCard, kept beside it rather than
 * inside it: a file that exports a component may not also export plain
 * functions without breaking fast refresh
 * (`react-refresh/only-export-components`), and each of these is a
 * contract worth a table-driven test of its own rather than one reachable
 * only through a rendered card.
 */

/** metricLabel names a value the way the host reports it: the mount or
 * unit it belongs to, then the metric. A whole-host metric has no item and
 * is just the metric name. */
export function metricLabel(v: HostCheckValue): string {
  return v.item ? `${v.item} ${v.metric}` : v.metric;
}

/** formatNumber keeps a reading readable without inventing precision the
 * agent did not report: at most two decimals, no trailing zeros, and no
 * locale-dependent grouping (these are percentages and load averages, not
 * currency). A raw load average is a float with sixteen significant
 * digits; rendered verbatim it is unreadable and implies an accuracy
 * nothing measured. */
export function formatNumber(value: number): string {
  return String(Math.round(value * 100) / 100);
}

/** isNumeric reports whether a value has a series behind it. A discrete
 * metric carries `label` instead of `value`, and GET /hosts/{id}/metrics
 * refuses it with a 400 — so this is also the test for whether a graph may
 * be offered at all. A numeric value whose latest collection failed still
 * qualifies: the history it would graph is real, only the newest reading
 * is missing. */
export function isNumeric(v: HostCheckValue): boolean {
  return v.label === undefined && typeof v.value === 'number';
}

/** sourceLabel renders the scope a check was inherited from as the thing
 * itself — the organization, the named monitoring group, this host — never
 * as the raw id the envelope carries beside it. */
export function sourceLabel(c: HostCheck): string {
  if (c.source === 'host') return 'This host';
  if (c.source === 'group') return c.source_name ?? 'Monitoring group';
  return 'Organization';
}
