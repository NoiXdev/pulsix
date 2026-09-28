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

/**
 * NUMERIC_PLUGIN says which plugins measure numbers. Only the service
 * plugin reports a label (the unit's state) — cpu, memory and disk report
 * numbers, and every one of them reports an error the same way
 * (internal/agent/checks: labelObservation is called from service.go
 * alone). It is a total map over the generated plugin union rather than a
 * list of exceptions, so a fifth plugin fails to compile here until
 * somebody decides which side of the line it is on.
 */
const NUMERIC_PLUGIN: Record<HostCheck['plugin'], boolean> = { cpu: true, memory: true, disk: true, service: false };

/**
 * isGraphable reports whether a graph may be offered for one of a check's
 * values, and it asks the CHECK, not the reading.
 *
 * A reading cannot answer it. When collection fails, check_current stores
 * an error row with neither value nor label (migration 00044's CHECK, and
 * internal/checks.Observation's own shape rule), so a rule of the form
 * "has a number" loses the graph button at exactly the moment an operator
 * wants it — the history GET /hosts/{id}/metrics would draw is still
 * there, and that route answers it during the incident too.
 *
 * So: a value that carries a `label` is discrete and has no series (the
 * metrics route refuses it with a 400, naming the label), and a check
 * whose plugin only ever produces labels has none either, however its
 * latest reading happens to have failed. Everything else is a number over
 * time.
 */
export function isGraphable(check: HostCheck, v: HostCheckValue): boolean {
  if (v.label !== undefined) return false;
  return NUMERIC_PLUGIN[check.plugin];
}

/** sourceLabel renders the scope a check was inherited from as the thing
 * itself — the organization, the named monitoring group, this host — never
 * as the raw id the envelope carries beside it. */
export function sourceLabel(c: HostCheck): string {
  if (c.source === 'host') return 'This host';
  if (c.source === 'group') return c.source_name ?? 'Monitoring group';
  return 'Organization';
}
