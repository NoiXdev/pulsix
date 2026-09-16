import type { Tone } from '../../ui/badge.tsx';

/** statusTone maps a job status onto the design's state colours: the
 * active pair (running/cancelling) reads accent/warn, the terminal pair
 * (succeeded/failed) ok/crit, cancelled and error per the state colours,
 * and everything still waiting reads unknown. */
export function statusTone(status: string): Tone {
  switch (status) {
    case 'succeeded':
      return 'ok';
    case 'running':
      return 'accent';
    case 'cancelling':
      return 'warn';
    case 'failed':
    case 'error':
      return 'crit';
    default:
      return 'unknown'; // queued, claimed, cancelled
  }
}

const KIND_LABEL: Record<string, string> = {
  run: 'run',
  adhoc: 'ad-hoc',
  connection_test: 'connection test',
  bootstrap: 'bootstrap',
  rotate_key: 'key rotation',
  revoke_key: 'old key removal',
  sync_repo: 'repo sync',
};

export function kindLabel(kind: string): string {
  return KIND_LABEL[kind] ?? kind;
}

/** shortId renders a UUID the way the run list and detail do: the first
 * block, monospace. */
export function shortId(id: string): string {
  return id.slice(0, 8);
}

export interface HostResultRow {
  host: string;
  ok: boolean;
  changed: boolean;
  failed: boolean;
  unreachable: boolean;
  skipped: boolean;
}

/** hostResultRows folds the run's stats (the worker's recap contract:
 * ok_hosts / changed_hosts / failed_hosts / unreachable_hosts /
 * skipped_hosts, each an array of host ids — or, for targets the claim
 * does not name, ansible host names) into one row per host with a boolean
 * per state, for the per-host result table. */
export function hostResultRows(stats: Record<string, unknown> | undefined | null): HostResultRow[] {
  const rows = new Map<string, HostResultRow>();
  for (const key of ['ok_hosts', 'changed_hosts', 'failed_hosts', 'unreachable_hosts', 'skipped_hosts'] as const) {
    const state = key.replace(/_hosts$/, '') as keyof Omit<HostResultRow, 'host'>;
    for (const host of stringList(stats?.[key])) {
      const row = rows.get(host) ?? { host, ok: false, changed: false, failed: false, unreachable: false, skipped: false };
      row[state] = true;
      rows.set(host, row);
    }
  }
  return [...rows.values()].sort((a, b) => a.host.localeCompare(b.host));
}

function stringList(raw: unknown): string[] {
  return Array.isArray(raw) ? raw.filter((v): v is string => typeof v === 'string') : [];
}

/** hasFailedHosts is the retry-failed button's gate: the run must have
 * failed (or unreachable) hosts for the action to have anything to do. */
export function hasFailedHosts(stats: Record<string, unknown> | undefined | null): boolean {
  return stringList(stats?.failed_hosts).length > 0 || stringList(stats?.unreachable_hosts).length > 0;
}
