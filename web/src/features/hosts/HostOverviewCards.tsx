import type { ReactNode } from 'react';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { useHostChecks } from '../../api/hooks/useChecks.ts';
import { useHostTest } from '../../api/hooks/useHostOps.ts';
import { useHostSSH } from '../../api/hooks/useSsh.ts';
import type { Host } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { routeLabel } from '../credentials/labels.ts';
import { checkCounts, type CheckCounts } from './hostOverview.ts';
import { useHostRoute } from './useHostRoute.ts';

const NONE = '—';

/** A section action that stays on the page (switches a tab, opens a
 * dialog): a button styled like the section links beside other titles. */
export function SectionAction({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="rounded text-xs text-muted-foreground hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
    >
      {children}
    </button>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-border py-1.5 last:border-b-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right break-words">{children}</dd>
    </div>
  );
}

/**
 * ConnectionSummary is the Overview's Connection card: how Pulsix logs in
 * (user@address:port), the route and the last connection test, read from
 * the same queries the Connection tab's cards read. "Change …" (for a role
 * that may change it) opens the SSH settings. An unmanaged host is not
 * reached over SSH, and the card says so.
 */
export function ConnectionSummary({ org, host, canChange, onChange }: { org: string; host: Host; canChange: boolean; onChange: () => void }) {
  const t = useT();
  const o = t.hosts.detail.overview;
  const ssh = useHostSSH(org, host.managed ? host.id : '');
  const test = useHostTest(org, host.managed ? host.id : '');
  const route = useHostRoute(org, host);
  const s = ssh.data?.ssh;
  const target = s ? `${s.username ? `${s.username}@` : ''}${host.address || host.name}:${s.port}` : NONE;
  const last = test.data?.test;

  return (
    <Section id="connection" title={o.connection} actions={host.managed && canChange ? <SectionAction onClick={onChange}>{o.change}</SectionAction> : undefined}>
      <Card className="px-4 py-2">
        {host.managed ? (
          <dl className="flex flex-col text-[13px]">
            <Row label={o.ssh}>
              <span className="font-mono">{target}</span>
            </Row>
            <Row label={o.route}>
              {route ? (
                <span className="inline-flex flex-wrap justify-end gap-x-1.5">
                  <span>{routeLabel(route.source, t)}</span>
                  {route.names.length > 0 && <span className="font-mono">{t.hosts.detail.fact.via(route.names.join(' → '))}</span>}
                </span>
              ) : (
                NONE
              )}
            </Row>
            <Row label={o.lastTest}>
              {last ? (
                <span className="inline-flex items-center gap-2">
                  <Pill tone={last.ok ? 'ok' : 'crit'}>{last.ok ? t.hosts.connection.ok : t.hosts.connection.failed}</Pill>
                  <span className="text-[11px] text-muted-foreground">{last.at.slice(0, 19).replace('T', ' ')}</span>
                </span>
              ) : test.isPending ? (
                NONE
              ) : (
                <span className="text-muted-foreground">{o.noTest}</span>
              )}
            </Row>
          </dl>
        ) : (
          <p className="py-1.5 text-[13px] text-muted-foreground">{t.hosts.detail.managementOff.body}</p>
        )}
      </Card>
    </Section>
  );
}

const ORDER: ReadonlyArray<keyof Omit<CheckCounts, 'total'>> = ['crit', 'warn', 'unknown', 'ok'];

/**
 * ChecksSummary is the Overview's Checks card: how many of the host's
 * checks stand at each verdict (only the verdicts some check has), and
 * "All N" to the Checks tab. `frozen` (an offline agent, backlog 3.57)
 * mutes the verdicts and says why. It reads the same host-checks query the
 * Checks tab and the key metrics read.
 */
export function ChecksSummary({ org, hostId, canRead, frozen = false, onAll }: { org: string; hostId: string; canRead: boolean; frozen?: boolean; onAll: () => void }) {
  const t = useT();
  const o = t.hosts.detail.overview;
  const checks = useHostChecks(org, hostId, canRead);
  const counts = checks.data ? checkCounts(checks.data.checks ?? []) : undefined;

  let body: ReactNode;
  if (!canRead) body = t.hosts.checks.noAccess;
  else if (checks.isError) body = checks.error instanceof Error ? checks.error.message : t.hosts.checks.readFailed;
  else if (!counts) body = t.common.loading;
  else if (counts.total === 0) body = o.noChecks;

  return (
    <Section
      id="checks"
      title={o.checks}
      actions={counts && counts.total > 0 ? <SectionAction onClick={onAll}>{o.allChecks(counts.total)}</SectionAction> : undefined}
    >
      <Card className="px-4 py-3">
        {body !== undefined || !counts ? (
          <p className="text-[13px] text-muted-foreground">{body}</p>
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              {ORDER.filter((k) => counts[k] > 0).map((k) => (
                <Pill key={k} tone={frozen ? 'unknown' : k}>
                  {o.count[k](counts[k])}
                </Pill>
              ))}
            </div>
            {frozen && <p className="mt-2 text-xs text-muted-foreground">{o.checksFrozen}</p>}
          </>
        )}
      </Card>
    </Section>
  );
}
