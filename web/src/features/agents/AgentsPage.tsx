import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Radio } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAgentTokens, useAgents, useRevokeAgent, useWithdrawAgentToken } from '../../api/hooks/useAgents.ts';
import { may } from '../../lib/roles.ts';
import type { Agent, AgentToken } from '../../api/types.ts';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';
import { AddAgentDialog } from './AddAgentDialog.tsx';

// The trailing 130px track is the Actions column, only rendered for
// canWrite — a read-only viewer gets no permanently blank column.
const COLUMNS_BASE = 'minmax(0, 0.9fr) minmax(0, 0.8fr) 90px 110px minmax(0, 0.5fr) 110px';
const COLUMNS_WITH_ACTIONS = `${COLUMNS_BASE} 130px`;
const TOKEN_COLUMNS = 'minmax(0, 1fr) 120px 110px 160px 110px';

/** AgentsPage lists the organization's host agents (v0.3a, spec §8):
 * which host each one runs on, whether it is still reporting (online =
 * a heartbeat within three heartbeat intervals), its version and its
 * certificate expiry — plus the enrollment tokens waiting to be redeemed.
 * Viewers read; operators enrol and revoke. */
export function AgentsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'agents.read');
  const canWrite = may(role, 'agents.write');
  const list = useAgents(org, !!orgInfo.data && canRead);
  const pending = useAgentTokens(org, !!orgInfo.data && canRead);
  const revoke = useRevokeAgent(org);
  const withdraw = useWithdrawAgentToken(org);
  const [adding, setAdding] = useState(false);

  const orgGuard = useQueryGuard(orgInfo);
  if (orgGuard) return orgGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return <p className="text-sm text-muted-foreground">You do not have access to this organization's agents.</p>;
  }
  if (!list.data) return <QueryLoading />;
  const agents = list.data.agents ?? [];
  const tokens = pending.data?.tokens ?? [];
  const now = new Date();
  const columns = canWrite ? COLUMNS_WITH_ACTIONS : COLUMNS_BASE;

  async function onRevoke(a: Agent) {
    if (!confirm(`Revoke the agent on ${a.host_name}? Its certificate is refused at once and the host stops being monitored.`)) return;
    try {
      await revoke.mutateAsync(a.host_id);
      toast.show(`Agent on ${a.host_name} revoked`);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Revoking failed', 'destructive');
    }
  }

  async function onWithdraw(t: AgentToken) {
    try {
      await withdraw.mutateAsync(t.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Withdrawing failed', 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Agents</h1>
          <span className="text-[13px] text-muted-foreground">
            {agents.filter((a) => a.status === 'online').length} of {agents.length} reporting · agents collect a host's monitoring data
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Radio size={15} /> Enrol agent
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={columns}>
          <span>Host</span>
          <span>Agent</span>
          <span>Status</span>
          <span>Last seen</span>
          <span>Version</span>
          <span>Certificate</span>
          {canWrite && <span>Actions</span>}
        </GridHeader>
        {agents.map((a) => {
          const expiring = !!a.cert_expires_at && daysLeft(a.cert_expires_at, now) < EXPIRY_WARN_DAYS;
          return (
            <GridRow key={a.host_id} columns={columns}>
              <span className="truncate text-[13px]" data-row>
                <Link to="/o/$org/hosts/$id" params={{ org, id: a.host_id }} className="hover:text-primary">
                  {a.host_name}
                </Link>
              </span>
              <span className="truncate font-mono text-xs">{a.agent_name}</span>
              <span>
                <Pill tone={a.status === 'online' ? 'ok' : 'warn'}>{a.status}</Pill>
              </span>
              <span className={`text-xs ${a.status === 'online' ? 'text-muted-foreground' : 'text-[var(--px-warn)]'}`}>{seenLabel(a.last_seen, now)}</span>
              <span className="truncate font-mono text-xs">{a.version || '—'}</span>
              <span className={`text-xs ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
                {expiryLabel(a.cert_expires_at, now)}
              </span>
              {canWrite && (
                <span className="flex gap-1">
                  <Button type="button" variant="destructive" aria-label={`Revoke agent on ${a.host_name}`} onClick={() => void onRevoke(a)}>
                    Revoke
                  </Button>
                </span>
              )}
            </GridRow>
          );
        })}
        {agents.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No agents enrolled yet.</p>}
      </Card>
      {tokens.length > 0 && (
        <Card>
          <GridHeader columns={TOKEN_COLUMNS}>
            <span>Pending enrollment token</span>
            <span>Uses</span>
            <span>Bound</span>
            <span>Expires</span>
            <span />
          </GridHeader>
          {tokens.map((t) => (
            <GridRow key={t.id} columns={TOKEN_COLUMNS}>
              <span className="truncate font-mono text-[13px]">{t.name_hint || t.id.slice(0, 8)}</span>
              <span className="text-xs text-muted-foreground">
                {t.uses} / {t.max_uses}
              </span>
              <span className="text-xs text-muted-foreground">{t.host_id ? 'to a host' : 'new host'}</span>
              <span className="text-xs text-muted-foreground">{new Date(t.expires_at).toLocaleString()}</span>
              {canWrite ? (
                <Button type="button" variant="ghost" aria-label={`Withdraw token ${t.name_hint || t.id.slice(0, 8)}`} onClick={() => void onWithdraw(t)}>
                  Withdraw
                </Button>
              ) : (
                <span />
              )}
            </GridRow>
          ))}
        </Card>
      )}
      {adding && <AddAgentDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
