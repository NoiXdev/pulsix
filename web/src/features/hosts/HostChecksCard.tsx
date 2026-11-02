import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { useHostChecks, useHostEffectiveRules, useMonitoringGroups, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { intervalLabel } from '../checks/interval.ts';
import { clockSkewLabel, sourceLabel, type TupleKey } from './hostChecks.ts';
import { HostCheckValueRow } from './HostCheckValueRow.tsx';
import { useT } from '../../i18n/useT.ts';

export interface HostChecksCardProps {
  org: string;
  hostId: string;
  /** `checks.read` — a viewer's right; without it nothing is requested. */
  canRead: boolean;
  /** `checks.write` — an operator's; the card itself edits nothing, so
   * this only decides whether the way to the editor is offered. */
  canWrite: boolean;
}

/**
 * HostChecksCard is the host page's monitoring section (v0.3b-1 spec,
 * "Host page, monitoring section"): the checks that actually run here
 * after org → group → host precedence, where each came from, what it last
 * measured, and a graph for the numeric ones.
 *
 * v0.3b-2 adds the rule engine's own verdict per tuple (`stateLabel`) and,
 * beside the Graph button, a "Rules" disclosure that answers "which rule
 * decided THIS value" from the value itself — review round 1's own
 * ruling, after the first version put every tuple's rules in a separate,
 * unfiltered list an operator had to scan instead. Only the one case that
 * disclosure cannot reach — an item-scoped rule whose item this host no
 * longer reports, which therefore has no row here to attach to — lives in
 * EffectiveRulesPanel instead.
 *
 * Three states the server distinguishes and this card must not flatten:
 *
 *  - `monitored: false` is its own state. The effective set does not
 *    depend on the flag — an unmonitored host still resolves to a definite
 *    list — but nothing runs it, so the readings are suppressed and the
 *    card says the host is not being watched. A list of checks with values
 *    beside them would read as live monitoring of a host nobody is
 *    watching.
 *  - A collection error is not a stale number. An error row carries no
 *    reading at all — check_current's own CHECK stores the error with
 *    neither value nor label — so the failure is what there is to render.
 *    What the error must not cost is the graph: the samples behind a
 *    numeric check are still there, and an incident is when they are
 *    worth looking at (see isGraphable).
 *  - `stale` is the server's answer (three of the check's own intervals
 *    without a reading, `internal/api`'s `staleIntervals`). The card
 *    renders the flag and never recomputes the rule: a second
 *    implementation would drift the moment the threshold moved. It does
 *    name the one cause the flag cannot distinguish on its own — a host
 *    clock that disagrees with the manager's, which marks every reading
 *    stale for ever while the agent is healthy (see clockSkewLabel).
 */
export function HostChecksCard({ org, hostId, canRead, canWrite }: HostChecksCardProps) {
  const t = useT();
  const query = useHostChecks(org, hostId, canRead);
  // One graph open at a time: these are the only reads on this page that
  // touch the sample hypertable.
  const [open, setOpen] = useState<MetricSelection | null>(null);
  // One rule disclosure open at a time, independent of the graph above —
  // an operator may want both open on different values, but the panel
  // itself grows tall enough per tuple that more than one at once per
  // value would read as a second scrolling problem this feature exists to
  // avoid.
  const [openRules, setOpenRules] = useState<TupleKey | null>(null);

  // Read alongside the checks themselves, gated on the same checks.read:
  // this is what makes each value's own "Rules" disclosure possible
  // without a second permission check per row. Not polled — see
  // useHostEffectiveRules' own doc comment.
  const effectiveRules = useHostEffectiveRules(org, hostId, canRead);
  const groups = useMonitoringGroups(org, canRead);
  const effective = effectiveRules.data?.effective ?? [];
  const groupList = groups.data?.groups ?? [];

  const monitored = query.data?.monitored ?? false;
  const checks = query.data?.checks ?? [];
  // Only while the host is monitored: with nothing being measured there
  // are no readings on the card for a clock to have skewed.
  const skew = monitored ? clockSkewLabel(query.data?.clock_offset_seconds, t) : null;

  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-semibold">{t.hosts.checks.title}</h2>
        {canWrite && (
          <Link to="/o/$org/checks" params={{ org }} className="text-xs text-muted-foreground hover:text-primary">
            {t.hosts.checks.manage}
          </Link>
        )}
      </div>

      {!canRead && <p className="text-xs text-muted-foreground">{t.hosts.checks.noAccess}</p>}
      {canRead && query.isError && (
        <p role="alert" className="text-xs text-destructive">
          {query.error instanceof Error ? query.error.message : t.hosts.checks.readFailed}
        </p>
      )}
      {canRead && !query.data && !query.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}

      {canRead && query.data && (
        <>
          {!monitored && (
            <p className="mb-3 text-xs text-[var(--px-warn)]">
              {t.hosts.checks.notMonitored}
            </p>
          )}
          {skew && (
            <p className="mb-3 text-xs text-[var(--px-warn)]">
              {t.hosts.checks.clockSkew(skew)}
            </p>
          )}
          {checks.length === 0 && <p className="text-xs text-muted-foreground">{t.hosts.checks.none}</p>}
          <ul className="flex flex-col gap-3">
            {checks.map((c) => (
              <li key={c.definition_id} className="rounded-md border border-border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="font-mono text-[13px] text-foreground">{c.key}</h3>
                  <Tag>{c.plugin}</Tag>
                  <Tag>{sourceLabel(c, t)}</Tag>
                  <span className="text-[11px] text-muted-foreground">{t.hosts.checks.every(intervalLabel(c.interval_seconds))}</span>
                  {!c.enabled && <Pill tone="unknown">{t.hosts.checks.disabled}</Pill>}
                </div>
                {monitored && (
                  <ul className="mt-2 flex flex-col gap-1.5">
                    {(c.values ?? []).map((v) => (
                      <HostCheckValueRow
                        key={`${v.item}:${v.metric}`}
                        org={org}
                        hostId={hostId}
                        canWrite={canWrite}
                        check={c}
                        value={v}
                        open={open}
                        onOpen={setOpen}
                        effective={effective}
                        groups={groupList}
                        openRules={openRules}
                        onOpenRules={setOpenRules}
                      />
                    ))}
                    {(c.values ?? []).length === 0 && (
                      <li className="text-[11px] text-muted-foreground">{t.hosts.checks.noReading}</li>
                    )}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
    </Card>
  );
}
