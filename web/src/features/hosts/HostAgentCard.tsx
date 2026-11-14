import { useId, useState } from 'react';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useHostAgent, useRevokeAgent } from '../../api/hooks/useAgents.ts';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';
import { VersionSkewBadge } from '../workers/VersionSkewBadge.tsx';
import { ConfigVersionBadge } from '../agents/ConfigVersionBadge.tsx';
import { AddAgentDialog } from '../agents/AddAgentDialog.tsx';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

/** HostAgentCard shows the agent running on this host (v0.3a, spec §8):
 * whether it is still reporting, which build it runs, when its
 * certificate expires — and, for an operator, the revoke action. A host
 * without an agent gets the enrolment hint instead: the read answers 404,
 * which is a state to render, not an error. */
export function HostAgentCard({ org, hostId, hostName, zoneId, canWrite }: { org: string; hostId: string; hostName: string; zoneId?: string; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const query = useHostAgent(org, hostId);
  const revoke = useRevokeAgent(org);
  const agent = query.data?.agent;
  const now = new Date();
  const [enrolling, setEnrolling] = useState(false);
  const problemsId = useId();

  async function onRevoke() {
    if (!agent) return;
    if (!(await ask({ message: t.agents.revokeConfirm(agent.host_name), confirmLabel: t.agents.revoke, destructive: true }))) return;
    try {
      await revoke.mutateAsync(hostId);
      toast.show(t.hosts.agent.revoked);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.revokeFailed, 'destructive');
    }
  }

  const expiring = !!agent?.cert_expires_at && daysLeft(agent.cert_expires_at, now) < EXPIRY_WARN_DAYS;
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-[13px] font-semibold">{t.hosts.agent.title}</h3>
        {agent && <Pill tone={agent.status === 'online' ? 'ok' : 'warn'}>{agent.status in t.agents.status ? t.agents.status[agent.status as keyof typeof t.agents.status] : agent.status}</Pill>}
      </div>
      {!agent && (
        <p className="text-xs text-muted-foreground">
          {t.hosts.agent.none}
        </p>
      )}
      {!agent && canWrite && !query.isPending && (
        <div className="mt-3 flex justify-end">
          <Button type="button" variant="primary" onClick={() => setEnrolling(true)}>
            {t.hosts.agent.enrol}
          </Button>
        </div>
      )}
      {enrolling && <AddAgentDialog org={org} host={{ id: hostId, name: hostName, zoneId }} onClose={() => setEnrolling(false)} />}
      {agent && (
        <div className="flex flex-col gap-2 text-xs">
          <div className="flex justify-between gap-2">
            <span className="text-muted-foreground">{t.hosts.agent.name}</span>
            <span className="truncate font-mono">{agent.agent_name}</span>
          </div>
          <div className="flex justify-between gap-2">
            <span className="text-muted-foreground">{t.agents.columns.lastSeen}</span>
            <span className={agent.status === 'online' ? '' : 'text-[var(--px-warn)]'}>{seenLabel(agent.last_seen, now, t)}</span>
          </div>
          <div className="flex justify-between gap-2">
            <span className="text-muted-foreground">{t.agents.columns.version}</span>
            <span className="flex min-w-0 flex-wrap items-center justify-end gap-x-2 gap-y-1">
              <span className="font-mono">{agent.version || '—'}</span>
              <VersionSkewBadge status={agent.version_status} />
              <ConfigVersionBadge reported={agent.config_version} expected={agent.config_expected} checkedAt={agent.config_checked_at} detail />
            </span>
          </div>
          <div className="flex justify-between gap-2">
            <span className="text-muted-foreground">{t.agents.columns.certificate}</span>
            <span className={expiring ? 'font-semibold text-[var(--px-warn)]' : ''} data-expiring={expiring || undefined}>
              {expiryLabel(agent.cert_expires_at, now, t)}
            </span>
          </div>
          <div className="flex flex-col gap-1">
            <div className="flex justify-between gap-2">
              <span className="text-muted-foreground">{t.hosts.agent.secrets}</span>
              {agent.secrets_state === 'absent' && <span>{t.hosts.agent.secretsAbsent}</span>}
              {agent.secrets_state === 'ok' && <span>{t.hosts.agent.secretsOk((agent.secret_names ?? []).length)}</span>}
            </div>
            {agent.secrets_state === 'ok' && (agent.secret_names ?? []).length > 0 && (
              <ul className="flex flex-wrap justify-end gap-1">
                {(agent.secret_names ?? []).map((n) => (
                  <li key={n} className="font-mono text-[11px] break-all text-muted-foreground">{n}</li>
                ))}
              </ul>
            )}
            {agent.secrets_state != null && agent.secrets_state !== 'ok' && agent.secrets_state !== 'absent' && (
              <p role="alert" className="wrap-anywhere text-[var(--px-warn)]">{agent.secrets_state}</p>
            )}
            {agent.secrets_state == null && <p className="text-muted-foreground">{t.hosts.agent.secretsUnknown}</p>}
          </div>
          {/* Backlog 3.5a: why checks defined in agent.yaml do not run, one
              line each as the agent reported them. Nothing for none, and
              nothing for null either — an agent older than agent.yaml, or
              one that has not sent a heartbeat, is the secrets' "unknown"
              above already. */}
          {(agent.local_check_problems ?? []).length > 0 && (
            <div className="flex flex-col gap-1">
              <span id={problemsId} className="text-muted-foreground">{t.hosts.agent.localCheckProblems}</span>
              <ul aria-labelledby={problemsId} className="flex flex-col gap-1">
                {(agent.local_check_problems ?? []).map((p, i) => (
                  <li key={i} className="wrap-anywhere text-[var(--px-warn)]">{p}</li>
                ))}
              </ul>
            </div>
          )}
          {canWrite && (
            <div className="mt-1 flex justify-end">
              <Button type="button" variant="destructive" aria-label={t.agents.revokeLabel(agent.host_name)} onClick={() => void onRevoke()}>
                {t.agents.revoke}
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
