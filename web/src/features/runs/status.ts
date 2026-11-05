import type { Tone } from '../../ui/badge.tsx';
import type { Messages } from '../../i18n/types.ts';

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

const CANCELLABLE = new Set(['queued', 'claimed', 'running']);

/** isCancellable says whether a run can still be cancelled: one waiting for
 * a worker or running. A cancelling run is already asked to stop. */
export function isCancellable(status: string): boolean {
  return CANCELLABLE.has(status);
}

export function kindLabel(kind: string, t: Messages): string {
  return (t.runs.kind as Record<string, string>)[kind] ?? kind;
}

/** statusLabel names a job status; a status this build does not know is
 * shown as the API sent it. */
export function statusLabel(status: string, t: Messages): string {
  return (t.runs.status as Record<string, string>)[status] ?? status;
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

export type CancelHostState = 'finished' | 'interrupted' | 'never-started';

export interface CancelHostRow {
  host: string;
  state: CancelHostState;
  task: string;
}

// The run event vocabulary (internal/worker/runner/events.go's mapping): a
// host's step opens on `task` and closes on one of these five. Every other
// state a run event can carry — `stdout` chief among them, the worker's
// follow-up line for the event it just emitted — takes no part in open/close
// and is skipped below.
const CANCEL_OPEN_STATE = 'task';
const CANCEL_CLOSING_STATES = new Set(['ok', 'changed', 'failed', 'skipped', 'unreachable']);

/**
 * cancelHostRows is hostResultRows's sibling for a cancelled run: a
 * cancelled run gets no terminal recap to fold (the worker stops before
 * playbook_on_stats), so this replays the run's own event log instead. A
 * host's last `task`/closing event decides its state: `task` with nothing
 * after it means the step was cut off mid-run (`interrupted`, carrying that
 * event's task name); a closing event last means the step finished before
 * the cancel landed (`finished`); a target no event mentions at all is
 * `never-started`. Events with an empty host, and any event whose state is
 * neither `task` nor a closing one, are ignored. A host the events name
 * that `targets` does not is included rather than dropped — the same
 * ansible-host-name-vs-host-id tolerance hostResultRows already has.
 * Sorted by host, localeCompare.
 *
 * This only works because useRunLog seeds the complete event history from
 * seq 0 before subscribing to the live stream, and Service.Events applies
 * no limit today — a capped history would hide interruptions that happened
 * before the cap, silently turning `interrupted` into `never-started`.
 *
 * It depends on one more property of useRunLog, for the same reason and in
 * the other direction: the log must be COMPLETE at the moment a run goes
 * terminal. The SSE stream can write its terminal status frame on top of
 * events still buffered server-side (internal/api/job_sse.go's select does
 * not order the two), and a missing tail here is not a visible gap — it is
 * a host whose closing `ok` never arrived, which this function reads as
 * `interrupted` and the page then warns about. useRunLog therefore does one
 * last fetchJobEvents before it reports a terminal status; if that drain is
 * ever removed, this function starts inventing interrupted hosts.
 */
export function cancelHostRows(
  targets: string[],
  events: { host: string; task: string; state: string }[],
): CancelHostRow[] {
  const last = new Map<string, { task: string; state: string }>();
  for (const e of events) {
    if (!e.host || (e.state !== CANCEL_OPEN_STATE && !CANCEL_CLOSING_STATES.has(e.state))) continue;
    last.set(e.host, { task: e.task, state: e.state });
  }
  const hosts = new Set<string>([...targets, ...last.keys()]);
  const rows: CancelHostRow[] = [...hosts].map((host) => {
    const final = last.get(host);
    if (!final) return { host, state: 'never-started', task: '' };
    if (final.state === CANCEL_OPEN_STATE) return { host, state: 'interrupted', task: final.task };
    return { host, state: 'finished', task: '' };
  });
  return rows.sort((a, b) => a.host.localeCompare(b.host));
}
