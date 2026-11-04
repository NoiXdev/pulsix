import type { Alert, HostCheck, HostCheckValue } from '../../api/types.ts';

/**
 * The host page's summaries: what its sidebar pill, its section nav and its
 * State tiles say about the host, derived from data other cards already
 * read. Plain functions, so the rules are tested without a page.
 */

export interface AlertSummary {
  /** Open alerts on the host. */
  open: number;
  /** Of those, the ones currently crit. */
  crit: number;
  /** crit when any is crit, warn when any is open (an unknown severity
   * reads as a warning: something is wrong, just not provably crit). */
  tone: 'ok' | 'warn' | 'crit';
}

export function summarizeAlerts(alerts: readonly Pick<Alert, 'severity'>[]): AlertSummary {
  const crit = alerts.filter((a) => a.severity === 'crit').length;
  const open = alerts.length;
  return { open, crit, tone: crit > 0 ? 'crit' : open > 0 ? 'warn' : 'ok' };
}

function fact(facts: Record<string, unknown> | null | undefined, key: string): string {
  const v = facts?.[key];
  if (typeof v === 'number') return String(v);
  return typeof v === 'string' ? v.trim() : '';
}

/**
 * systemLabel names the operating system from the host's facts. The agent
 * reports `os_version` as platform plus version ("debian 13"); facts set
 * by other means may carry `distribution` or `os` and a separate
 * `version`. Null when none of them says anything.
 */
export function systemLabel(facts: Record<string, unknown> | null | undefined): string | null {
  const osVersion = fact(facts, 'os_version');
  if (osVersion) return osVersion;
  const name = fact(facts, 'distribution') || fact(facts, 'os');
  if (!name) return null;
  const version = fact(facts, 'version');
  return version ? `${name} ${version}` : name;
}

export type TileKind = 'cpu' | 'ram' | 'disk' | 'load';
export type TileTone = 'ok' | 'warn' | 'crit' | 'unknown';

/** One State tile: the series it graphs and the reading it shows. */
export interface StateTile {
  kind: TileKind;
  check: string;
  item: string;
  metric: string;
  value: number;
  tone: TileTone;
}

/** A reading's colour: the rule engine's verdict, unless the reading or
 * the verdict is too old to stand for the host's state now, or there is
 * no verdict at all. */
function toneOf(v: HostCheckValue): TileTone {
  if (v.stale || v.state_stale || !v.state) return 'unknown';
  return v.state;
}

interface Candidate {
  check: HostCheck;
  value: HostCheckValue & { value: number };
}

/** The numeric readings of `metric` from the enabled checks of `plugin`. */
function readings(checks: readonly HostCheck[], plugin: HostCheck['plugin'], metric: string): Candidate[] {
  const out: Candidate[] = [];
  for (const c of checks) {
    if (!c.enabled || c.plugin !== plugin) continue;
    for (const v of c.values ?? []) {
      if (v.metric === metric && typeof v.value === 'number' && !v.error) out.push({ check: c, value: v as Candidate['value'] });
    }
  }
  return out;
}

function tile(kind: TileKind, c: Candidate | undefined): StateTile[] {
  if (!c) return [];
  return [{ kind, check: c.check.key, item: c.value.item, metric: c.value.metric, value: c.value.value, tone: toneOf(c.value) }];
}

/**
 * stateTiles picks the State section's tiles from the host's checks, by
 * plugin rather than by check key (keys are the operator's to choose): CPU
 * (`cpu.used_pct`), RAM (`mem.used_pct`), the fullest disk (the mount
 * whose `disk.used_pct` is highest, among the non-stale readings while
 * there is one — a mount that stopped reporting must not hide a current
 * one — and among the stale ones only when none is current) and load
 * (`cpu.load1`, only when a check reports it). A tile without a numeric
 * reading is left out.
 */
export function stateTiles(checks: readonly HostCheck[]): StateTile[] {
  const wholeHost = (plugin: HostCheck['plugin'], metric: string) => readings(checks, plugin, metric).find((c) => c.value.item === '');
  const disks = readings(checks, 'disk', 'disk.used_pct');
  const current = disks.filter((c) => !c.value.stale);
  const fullestDisk = (current.length > 0 ? current : disks).reduce<Candidate | undefined>(
    (best, c) => (best === undefined || c.value.value > best.value.value ? c : best),
    undefined,
  );
  return [
    ...tile('cpu', wholeHost('cpu', 'cpu.used_pct')),
    ...tile('ram', wholeHost('memory', 'mem.used_pct')),
    ...tile('disk', fullestDisk),
    ...tile('load', wholeHost('cpu', 'cpu.load1')),
  ];
}
