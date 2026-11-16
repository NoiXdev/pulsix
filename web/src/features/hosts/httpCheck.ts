import type { HostCheck, HostCheckValue } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import type { AssertionTableRow } from '../../ui/AssertionTable.tsx';
import type { StackedBarPart } from '../../ui/StackedBar.tsx';
import { composeExpression, entriesOf, rowFromEntry } from '../checks/assertionRows.ts';
import { assertionActualText, assertionResult, failureText } from '../checks/syntheticLabels.ts';
import { formatNumber } from './hostChecks.ts';

/**
 * The host page's reading of a synthetic check beyond its plain values
 * (backlog 3.13b): why it is down, beside `<plugin>.up`; and, for an http
 * check, the latest attempt's timing breakdown and one row per assertion.
 * Pure, like hostChecks.ts: the server's readings are worded, never
 * recomputed.
 */

/** The phases of an http attempt, in the order they happen. */
const PHASES = ['dns', 'connect', 'tls', 'ttfb', 'transfer'] as const;

/** reading: a whole-host value of the check that is current — not an
 * error and not stale, so a phase an earlier URL had (dns_ms of a name
 * since replaced by an address) is not mixed into the latest attempt. */
function current(check: HostCheck, metric: string, item = ''): HostCheckValue | undefined {
  return (check.values ?? []).find((v) => v.item === item && v.metric === metric && v.error === undefined && !v.stale);
}

/** failureBeside is the failure reason to show next to a synthetic
 * check's `<plugin>.up`, in words; null for any other value, and while
 * the check is up (`none`) or its reason is not known. */
export function failureBeside(check: HostCheck, value: HostCheckValue, t: Messages): string | null {
  if (value.metric !== `${check.plugin}.up`) return null;
  const failure = current(check, `${check.plugin}.failure`, value.item);
  if (!failure?.label || failure.label === 'none') return null;
  return failureText(t, failure.label);
}

/** timingParts is the latest attempt's breakdown as bar parts: each phase
 * the check reported, in order; a phase without a current reading (no
 * name to resolve, no TLS) is left out. */
export function timingParts(check: HostCheck, t: Messages): StackedBarPart[] {
  const parts: StackedBarPart[] = [];
  for (const phase of PHASES) {
    const v = current(check, `http.${phase}_ms`);
    if (v?.value === undefined) continue;
    parts.push({ key: phase, label: t.hosts.http.phases[phase], value: v.value, display: t.hosts.vantages.ms(formatNumber(v.value)) });
  }
  return parts;
}

function valueOf(check: HostCheck, metric: string, item: string): HostCheckValue | undefined {
  return (check.values ?? []).find((v) => v.item === item && v.metric === metric);
}

/** assertionRows are the check's assertions as table rows, in the order
 * its `assertions` parameter lists them: the expression and severity from
 * the entry, the status and the value seen from the latest attempt. */
export function assertionRows(check: HostCheck, t: Messages, format: Format, now: Date): AssertionTableRow[] {
  const raw = check.params?.assertions;
  const entries = Array.isArray(raw) ? entriesOf(raw.filter((e): e is string => typeof e === 'string').join('\n')) : [];
  return entries.map((entry) => {
    const eq = entry.indexOf('=');
    const name = entry.slice(0, eq);
    const row = rowFromEntry(entry);
    const result = valueOf(check, 'http.assertion', name);
    const actual = valueOf(check, 'http.assertion_actual', name);
    let status: AssertionTableRow['status'] = { label: t.hosts.http.noResult, tone: 'unknown' };
    if (result?.error !== undefined) status = { label: t.hosts.http.notEvaluated, tone: 'unknown' };
    else if (result?.label) status = assertionResult(t, result.label);
    let seen = '—';
    if (actual?.error !== undefined) seen = actual.error;
    else if (actual?.label !== undefined) seen = assertionActualText(t, actual.label);
    const at = result?.at ?? actual?.at;
    const age = at ? format.relative(at, now) : '—';
    return {
      key: name,
      name,
      expression: row ? composeExpression(row) : entry.slice(eq + 1),
      severity: t.checks.severity[row?.severity ?? 'crit'],
      status,
      actual: seen,
      age: result?.stale ? t.hosts.http.staleAge(age) : age,
    };
  });
}
