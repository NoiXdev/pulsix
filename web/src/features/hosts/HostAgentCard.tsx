import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useHostAgent, useRevokeAgent } from '../../api/hooks/useAgents.ts';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';
import { useT } from '../../i18n/useT.ts';

/** HostAgentCard shows the agent running on this host (v0.3a, spec §8):
 * whether it is still reporting, which build it runs, when its
 * certificate expires — and, for an operator, the revoke action. A host
 * without an agent gets the enrolment hint instead: the read answers 404,
 * which is a state to render, not an error. */
export function HostAgentCard({ org, hostId, canWrite }: { org: string; hostId: string; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const query = useHostAgent(org, hostId);
  const revoke = useRevokeAgent(org);
  const agent = query.data?.agent;
  const now = new Date();

  async function onRevoke() {
    if (!agent) return;
    if (!confirm(t.agents.revokeConfirm(agent.host_name))) return;
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
        <h2 className="text-[13px] font-semibold">{t.hosts.agent.title}</h2>
        {agent && <Pill tone={agent.status === 'online' ? 'ok' : 'warn'}>{agent.status in t.agents.status ? t.agents.status[agent.status as keyof typeof t.agents.status] : agent.status}</Pill>}
      </div>
      {!agent && (
        <p className="text-xs text-muted-foreground">
          {t.hosts.agent.none}
        </p>
      )}
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
            <span className="font-mono">{agent.version || '—'}</span>
          </div>
          <div className="flex justify-between gap-2">
            <span className="text-muted-foreground">{t.agents.columns.certificate}</span>
            <span className={expiring ? 'font-semibold text-[var(--px-warn)]' : ''} data-expiring={expiring || undefined}>
              {expiryLabel(agent.cert_expires_at, now, t)}
            </span>
          </div>
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
