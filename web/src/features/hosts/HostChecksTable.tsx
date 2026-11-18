import { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { Pill, Tag, type Tone } from '../../ui/badge.tsx';
import type { MetricSelection } from '../../api/hooks/useChecks.ts';
import type { CheckPluginDescriptor, EffectiveRule, HostCheck, MonitoringGroup } from '../../api/types.ts';
import { intervalLabel } from '../checks/interval.ts';
import { checkStatus, isFailing, keyValue, orderChecks, type CheckStatus } from './checkSummary.ts';
import { HostCheckDetail } from './HostCheckDetail.tsx';
import { missingSecretText, sourceLabel, type MetricKinds, type TupleKey } from './hostChecks.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';

export interface HostChecksTableProps {
  org: string;
  hostId: string;
  canWrite: boolean;
  frozen: boolean;
  /** false: nothing measures these checks, so there are no readings to open. */
  monitored: boolean;
  checks: readonly HostCheck[];
  plugins: readonly CheckPluginDescriptor[] | undefined;
  kinds: MetricKinds;
  effective: EffectiveRule[];
  groups: MonitoringGroup[];
}

const STATUS_TONE: Record<CheckStatus, Tone> = { crit: 'crit', warn: 'warn', ok: 'ok', unknown: 'unknown' };

function statusText(s: CheckStatus, t: Messages): string {
  return t.hosts.state[s];
}

/**
 * HostChecksTable is the Checks tab's overview: one row per check with its
 * source, interval, worst verdict and a key value; a row opens to the
 * check's readings (HostCheckDetail). Failing checks come first. What is
 * open is local state: nothing, except the first failing check when the
 * table first shows — so mount this once the checks are known.
 */
export function HostChecksTable({ org, hostId, canWrite, frozen, monitored, checks, plugins, kinds, effective, groups }: HostChecksTableProps) {
  const t = useT();
  const ordered = orderChecks(checks, frozen);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => {
    const first = !frozen && monitored ? ordered.find(isFailing) : undefined;
    return new Set(first ? [first.key] : []);
  });
  // One graph and one rule disclosure open at a time across the table:
  // the graph reads the sample hypertable, and a second panel per value
  // would read as a scrolling problem.
  const [open, setOpen] = useState<MetricSelection | null>(null);
  const [openRules, setOpenRules] = useState<TupleKey | null>(null);
  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  const c = t.hosts.checksTab.columns;

  return (
    <div className="overflow-x-auto">
      <table aria-label={t.hosts.checksTab.tableLabel} className="w-full border-collapse text-left text-xs">
        <thead className="text-[11px] uppercase tracking-wide text-muted-foreground">
          <tr>
            <th scope="col" className="py-1.5 pr-3 font-medium">{c.check}</th>
            <th scope="col" className="py-1.5 pr-3 font-medium">{c.source}</th>
            <th scope="col" className="py-1.5 pr-3 font-medium">{c.interval}</th>
            <th scope="col" className="py-1.5 pr-3 font-medium">{c.status}</th>
            <th scope="col" className="py-1.5 font-medium">{c.value}</th>
          </tr>
        </thead>
        {/* Keyed by the check key, unique on a host: a check defined in the
            host's agent.yaml (backlog 3.5a) has no definition id, and the
            server lists it in place of a definition with the same key. */}
        {ordered.map((check) => {
          const isOpen = monitored && expanded.has(check.key);
          const status = monitored && !frozen ? checkStatus(check) : 'unknown';
          const value = monitored ? keyValue(check, t) : null;
          const detailId = `check-detail-${check.key}`;
          return (
            <tbody key={check.key} className="border-t border-border align-top">
              <tr>
                <td className="py-2 pr-3">
                  <h3 className="font-mono text-[13px] text-foreground">
                    {monitored ? (
                      <button
                        type="button"
                        aria-expanded={isOpen}
                        aria-controls={isOpen ? detailId : undefined}
                        title={isOpen ? t.hosts.checksTab.collapse(check.key) : t.hosts.checksTab.expand(check.key)}
                        onClick={() => toggle(check.key)}
                        className="inline-flex items-center gap-1 rounded text-left hover:text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
                      >
                        <ChevronRight size={14} aria-hidden className={isOpen ? 'rotate-90 transition-transform' : 'transition-transform'} />
                        {check.key}
                      </button>
                    ) : (
                      check.key
                    )}
                  </h3>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                    <Tag>{check.plugin}</Tag>
                    {/* A local check is always on — only removing it from
                        agent.yaml stops it — and nothing here edits,
                        disables or overrides it: no definition is behind it. */}
                    {!check.enabled && check.source !== 'local' && <Pill tone="unknown">{t.hosts.checks.disabled}</Pill>}
                  </div>
                  {check.missing_secret && (
                    <p role="alert" className="mt-1 text-xs text-[var(--px-warn)]">
                      {missingSecretText(t, check.missing_secret, check.plugin, plugins)}
                    </p>
                  )}
                </td>
                <td className="py-2 pr-3"><Tag>{sourceLabel(check, t)}</Tag></td>
                <td className="py-2 pr-3 text-muted-foreground">{t.hosts.checks.every(intervalLabel(check.interval_seconds, t))}</td>
                <td className="py-2 pr-3">
                  {monitored && frozen ? (
                    <span className="text-muted-foreground">{t.hosts.checksTab.status.notJudged}</span>
                  ) : (
                    <Pill tone={STATUS_TONE[status]}>{statusText(status, t)}</Pill>
                  )}
                </td>
                <td className={frozen ? 'py-2 text-muted-foreground' : 'py-2 text-foreground'}>{value ?? '—'}</td>
              </tr>
              {isOpen && (
                <tr>
                  <td id={detailId} colSpan={5} className="pb-3">
                    <HostCheckDetail
                      org={org}
                      hostId={hostId}
                      canWrite={canWrite}
                      frozen={frozen}
                      check={check}
                      plugins={plugins}
                      kinds={kinds}
                      open={open}
                      onOpen={setOpen}
                      effective={effective}
                      groups={groups}
                      openRules={openRules}
                      onOpenRules={setOpenRules}
                    />
                  </td>
                </tr>
              )}
            </tbody>
          );
        })}
      </table>
    </div>
  );
}
