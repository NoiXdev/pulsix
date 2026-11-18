import type { HostCheck, HostCheckValue } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { formatNumber } from './hostChecks.ts';

/**
 * What the Checks tab's table says about one check at a glance: its worst
 * verdict, the order the rows come in, and one short key value. Pure, like
 * hostChecks.ts. Nothing here measures or judges anything — the verdicts
 * and readings are the server's, only picked and worded.
 */

export type CheckStatus = 'crit' | 'warn' | 'ok' | 'none';

/** checkStatus is the worst verdict among a check's items. An ignored
 * item is not judged (its state is meaningless) and `unknown`/never
 * evaluated carry no verdict, so a check with nothing else is `none`. */
export function checkStatus(check: HostCheck): CheckStatus {
  let worst: CheckStatus = 'none';
  for (const v of check.values ?? []) {
    if (v.ignored) continue;
    if (v.state === 'crit') return 'crit';
    if (v.state === 'warn') worst = 'warn';
    else if (v.state === 'ok' && worst === 'none') worst = 'ok';
  }
  return worst;
}

/** isFailing: warning or critical. */
export function isFailing(check: HostCheck): boolean {
  const s = checkStatus(check);
  return s === 'crit' || s === 'warn';
}

/** orderChecks puts the failing checks first and keeps the given order
 * otherwise. While the verdicts are frozen (an offline client) they are
 * from before and are not a reason to reorder. */
export function orderChecks(checks: readonly HostCheck[], frozen: boolean): HostCheck[] {
  if (frozen) return [...checks];
  return [...checks.filter(isFailing), ...checks.filter((c) => !isFailing(c))];
}

function reading(check: HostCheck, metric: string): HostCheckValue | undefined {
  return (check.values ?? []).find((v) => v.metric === metric && v.error === undefined && v.value !== undefined);
}

/** The metric a plugin's one-number summary comes from, with how it reads. */
const MS_METRIC: Record<string, string> = { icmp: 'icmp.rtt_ms', tcp: 'tcp.response_ms', dns: 'dns.resolve_ms' };
const PERCENT_METRIC: Record<string, string> = { cpu: 'cpu.used_pct', memory: 'memory.used_pct' };

/** keyValue is a short summary of a check from what it already measured,
 * or null when it has no usable reading. */
export function keyValue(check: HostCheck, t: Messages): string | null {
  const w = t.hosts.checksTab.value;
  const num = (v: HostCheckValue | undefined) => (v?.value === undefined ? undefined : formatNumber(v.value));
  switch (check.plugin) {
    case 'http': {
      const code = num(reading(check, 'http.status_code'));
      if (code !== undefined) return w.http(code, ((ms) => (ms === undefined ? '' : w.ms(ms)))(num(reading(check, 'http.response_ms'))));
      break;
    }
    case 'disk': {
      const used = (check.values ?? []).filter((v) => v.metric === 'disk.used_pct' && v.error === undefined && v.value !== undefined);
      if (used.length > 0) {
        const worst = used.reduce((a, b) => ((b.value ?? 0) > (a.value ?? 0) ? b : a));
        const n = formatNumber(worst.value ?? 0);
        return worst.item ? w.percentOn(n, worst.item) : w.percent(n);
      }
      break;
    }
    case 'tls': {
      const days = num(reading(check, 'tls.cert_expiry_days'));
      if (days !== undefined) return w.daysLeft(days);
      break;
    }
    case 'cpu':
    case 'memory': {
      const n = num(reading(check, PERCENT_METRIC[check.plugin]));
      if (n !== undefined) return w.percent(n);
      break;
    }
    case 'icmp':
    case 'tcp':
    case 'dns': {
      const n = num(reading(check, MS_METRIC[check.plugin]));
      if (n !== undefined) return w.ms(n);
      break;
    }
  }
  const first = (check.values ?? []).find((v) => v.error === undefined && (v.value !== undefined || v.label !== undefined));
  if (!first) return null;
  return w.metric(first.metric, first.label ?? formatNumber(first.value ?? 0));
}
