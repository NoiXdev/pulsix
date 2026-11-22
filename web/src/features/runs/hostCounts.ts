/**
 * A run's recap counts per host (backlog 3.126): the worker writes
 * `stats.host_counts` — each host (its id, or ansible's name for a host the
 * claim did not name) → ok / changed / skipped / failed / unreachable. Runs
 * older than that have none. The stats are an untyped map, so this reads
 * them defensively: anything that is not a count reads as zero.
 */

export interface RecapCounts {
  ok: number;
  changed: number;
  skipped: number;
  failed: number;
  unreachable: number;
}

export const ZERO_COUNTS: Readonly<RecapCounts> = Object.freeze({ ok: 0, changed: 0, skipped: 0, failed: 0, unreachable: 0 });

const KEYS = ['ok', 'changed', 'skipped', 'failed', 'unreachable'] as const;

const isMap = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);

function count(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? Math.floor(v) : 0;
}

/** readHostCounts answers each host's counts, or null when the run's stats
 * carry no host_counts (an older run, or one without a recap yet). */
export function readHostCounts(stats: Record<string, unknown> | null | undefined): Map<string, RecapCounts> | null {
  const raw = stats?.host_counts;
  if (!isMap(raw)) return null;
  const out = new Map<string, RecapCounts>();
  for (const [host, value] of Object.entries(raw)) {
    if (!isMap(value)) continue;
    out.set(host, { ok: count(value.ok), changed: count(value.changed), skipped: count(value.skipped), failed: count(value.failed), unreachable: count(value.unreachable) });
  }
  return out;
}

/** countsOfHost is one host's counts; a host the map does not name counts zero. */
export function countsOfHost(stats: Record<string, unknown> | null | undefined, host: string): RecapCounts {
  return readHostCounts(stats)?.get(host) ?? { ...ZERO_COUNTS };
}

/** sumHostCounts adds the counts over every host; null without host_counts. */
export function sumHostCounts(stats: Record<string, unknown> | null | undefined): RecapCounts | null {
  const counts = readHostCounts(stats);
  if (!counts) return null;
  const sum: RecapCounts = { ...ZERO_COUNTS };
  for (const c of counts.values()) for (const k of KEYS) sum[k] += c[k];
  return sum;
}
