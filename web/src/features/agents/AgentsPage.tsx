import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Radio } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAgentTokens, useAgents, useRevokeAgent, useWithdrawAgentToken } from '../../api/hooks/useAgents.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import type { Agent, AgentToken } from '../../api/types.ts';
import { AgentRow, AgentTokenRow } from './AgentRow.tsx';
import { AddAgentDialog } from './AddAgentDialog.tsx';

// The trailing 40px track holds a row's ⋯ menu. Every agent row has one (Open
// host is always there); a token row only has one for a writer.
const COLUMNS = 'minmax(0, 0.9fr) minmax(0, 0.8fr) 90px 110px minmax(0, 0.9fr) 110px 40px';
const TOKEN_COLUMNS = 'minmax(0, 1fr) 120px 110px 160px';

/** AgentsPage lists the organization's host agents (v0.3a, spec §8):
 * which host each one runs on, whether it is still reporting (online =
 * a heartbeat within three heartbeat intervals), its version and its
 * certificate expiry — plus the enrollment tokens waiting to be redeemed.
 * Viewers read; operators enrol and revoke. */
export function AgentsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
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
    return <p className="text-sm text-muted-foreground">{t.agents.noAccess}</p>;
  }
  if (!list.data) return <QueryLoading />;
  const agents = list.data.agents ?? [];
  const tokens = pending.data?.tokens ?? [];
  const now = new Date();
  const tokenColumns = canWrite ? `${TOKEN_COLUMNS} 40px` : TOKEN_COLUMNS;

  async function onRevoke(a: Agent) {
    if (!confirm(t.agents.revokeConfirm(a.host_name))) return;
    try {
      await revoke.mutateAsync(a.host_id);
      toast.show(t.agents.revoked(a.host_name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.revokeFailed, 'destructive');
    }
  }

  async function onWithdraw(tok: AgentToken) {
    try {
      await withdraw.mutateAsync(tok.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.withdrawFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.agents.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.agents.summary(agents.filter((a) => a.status === 'online').length, agents.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Radio size={15} /> {t.agents.enrol}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.agents.columns.host}</span>
          <span>{t.agents.columns.agent}</span>
          <span>{t.agents.columns.status}</span>
          <span>{t.agents.columns.lastSeen}</span>
          <span>{t.agents.columns.version}</span>
          <span>{t.agents.columns.certificate}</span>
          <span />
        </GridHeader>
        {agents.map((a) => (
          <AgentRow key={a.host_id} org={org} agent={a} columns={COLUMNS} now={now} canWrite={canWrite} onRevoke={(x) => void onRevoke(x)} />
        ))}
        {agents.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.agents.empty}</p>}
      </Card>
      {tokens.length > 0 && (
        <Card>
          <GridHeader columns={tokenColumns}>
            <span>{t.agents.columns.pendingToken}</span>
            <span>{t.agents.columns.uses}</span>
            <span>{t.agents.columns.bound}</span>
            <span>{t.agents.columns.expires}</span>
            {canWrite && <span />}
          </GridHeader>
          {tokens.map((tok) => (
            <AgentTokenRow key={tok.id} token={tok} columns={tokenColumns} canWrite={canWrite} onWithdraw={(x) => void onWithdraw(x)} />
          ))}
        </Card>
      )}
      {adding && <AddAgentDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
