import type { ReactNode } from 'react';
import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { JobDetail, JobEvent } from '../../api/types.ts';
import { cancelHostRows, hostResultRows } from './status.ts';
import type { CancelHostState } from './status.ts';
import { isTerminal } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';

// cancelHostRows answers a question about the host's STEP, not about the
// play: `finished` there means "this host's last event closed its step",
// which in a linear-strategy play is just as true after task 3 of 10 as
// after the last one. Labelling that bare "finished" invited an operator
// to re-run only the interrupted host and leave a half-applied one alone,
// so the label (t.runs.cancelState) says what was actually observed.
const CANCEL_STATE_COLOR: Record<CancelHostState, string> = {
  finished: 'var(--px-unknown)',
  interrupted: 'var(--px-warn)',
  'never-started': 'var(--px-unknown)',
};

/**
 * RunResultSection is what the run did: the run's error, the per-host table
 * folded from its recap and, for a cancelled run, what each target host was
 * left doing with the warnings that go with it. The table stays a table:
 * the recap names hosts by their inventory name, not their id, so there is
 * no host page to link a row to. A run with none of these yet (still
 * queued, or running before its recap) says so in one line.
 */
export function RunResultSection({ job, status, events }: { job: JobDetail; status: string; events: readonly JobEvent[] }) {
  const t = useT();
  const rows = hostResultRows(job.stats);
  // A cancelled run gets no Ansible recap (the worker stops before
  // playbook_on_stats runs), so the per-host table renders empty;
  // this derives what each target host was left doing from the event log
  // itself, which useRunLog seeds in full before anything else happens.
  const cancelRows = status === 'cancelled' ? cancelHostRows(job.hosts ?? [], [...events]) : [];

  return (
    <Section id="result" title={t.runs.detail.section.result}>
      {job.error && (
        <div role="alert" className="rounded-md border p-3 text-xs [overflow-wrap:anywhere]" style={{ borderColor: 'color-mix(in srgb, var(--px-crit) 33%, transparent)', background: 'color-mix(in srgb, var(--px-crit) 10%, transparent)', color: 'var(--px-crit)' }}>
          {job.error}
        </div>
      )}
      {rows.length > 0 && (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full text-xs" aria-label={t.runs.detail.perHost}>
              <thead>
                <tr className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
                  <th className="px-4 py-2 text-left">{t.runs.detail.hostColumn}</th>
                  <th className="px-2 py-2">{t.runs.detail.columns.ok}</th>
                  <th className="px-2 py-2">{t.runs.detail.columns.changed}</th>
                  <th className="px-2 py-2">{t.runs.detail.columns.failed}</th>
                  <th className="px-2 py-2">{t.runs.detail.columns.unreachable}</th>
                  <th className="px-2 py-2">{t.runs.detail.columns.skipped}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.host} className="border-t border-border">
                    <td className="px-4 py-1.5 font-mono text-[13px] [overflow-wrap:anywhere]">{r.host}</td>
                    <StateCell on={r.ok} color="var(--px-ok)" label={t.runs.detail.hostState.ok} />
                    <StateCell on={r.changed} color="var(--px-accent)" label={t.runs.detail.hostState.changed} />
                    <StateCell on={r.failed} color="var(--px-crit)" label={t.runs.detail.hostState.failed} />
                    <StateCell on={r.unreachable} color="var(--px-crit)" label={t.runs.detail.hostState.unreachable} />
                    <StateCell on={r.skipped} color="var(--px-unknown)" label={t.runs.detail.hostState.skipped} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      {cancelRows.length > 0 && (
        <Card>
          <div className="border-b border-border px-4 py-2 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
            {t.runs.detail.cancelledRun}
          </div>
          {/* Warn only where a command was in flight — when the worker
              that claimed the run said a cancel stops the play on it
              (cancel_stops_play, backlog 2.113). Backlog 2.107 made that
              true for a worker running plays under the play supervisor
              (spec 2026-10-29-cancel-stops-the-play-design.md, "What the
              run view shows"), so a host that was between tasks or never
              started has nothing left running and a banner there would be
              noise that trains operators to skip the one that matters. An
              interrupted host is the exception: the command already
              started on it is not undone by the stop.
              Without that word — an older worker, one without the
              supervisor, a run requeued since or claimed before the field
              existed — the play may still be running against every host,
              so the warning covers all of them, as it did before 2.107.
              The pill for a host with no task in flight is neutral, not
              green: "finished" there only says its last step closed, not
              that the play finished. */}
          {!job.cancel_stops_play ? (
            <CancelWarning>
              {t.runs.detail.unconfirmedBefore}
              <strong>{t.runs.detail.unconfirmedStrong}</strong>
              {t.runs.detail.unconfirmedAfter}
            </CancelWarning>
          ) : (
            cancelRows.some((r) => r.state === 'interrupted') && (
              <CancelWarning>
                {t.runs.detail.stopped}
              </CancelWarning>
            )
          )}
          <ul className="flex flex-col gap-1 p-4 text-xs" aria-label={t.runs.detail.cancelledHosts}>
            {cancelRows.map((r) => (
              <li key={r.host} className="flex items-center justify-between gap-2">
                <span className="min-w-0 font-mono [overflow-wrap:anywhere]">{r.host}</span>
                <span className="flex shrink-0 items-center gap-2">
                  {r.state === 'interrupted' && <span className="text-muted-foreground">{r.task}</span>}
                  <span className="font-semibold" style={{ color: CANCEL_STATE_COLOR[r.state] }}>
                    {t.runs.cancelState[r.state]}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      {!job.error && rows.length === 0 && cancelRows.length === 0 && (
        <p className="text-xs text-muted-foreground">
          {isTerminal(status) ? t.runs.detail.noResultsFinal : t.runs.detail.noResults}
        </p>
      )}
    </Section>
  );
}

function CancelWarning({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="m-4 rounded-md border p-3 text-xs"
      style={{ borderColor: 'color-mix(in srgb, var(--px-warn) 33%, transparent)', background: 'color-mix(in srgb, var(--px-warn) 10%, transparent)', color: 'var(--px-warn)' }}
    >
      {children}
    </div>
  );
}

function StateCell({ on, color, label }: { on: boolean; color: string; label: string }) {
  return (
    <td className="px-2 py-1.5 text-center">
      {on ? (
        <span className="font-mono text-[11px] font-semibold" style={{ color }}>
          {label}
        </span>
      ) : (
        <span className="text-muted-foreground">·</span>
      )}
    </td>
  );
}
