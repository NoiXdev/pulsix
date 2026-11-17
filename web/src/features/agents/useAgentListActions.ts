import { useNavigate } from '@tanstack/react-router';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useRevokeAgent, useWithdrawAgentToken } from '../../api/hooks/useAgents.ts';
import type { Agent, AgentToken } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * useAgentListActions holds the Agents page's two row menus. An agent: Open
 * host (its Connection section) for every role and, for a writer, Revoke…
 * (which asks first). A pending token: Withdraw for a writer, without a
 * question (an unredeemed token has nothing to lose); a reader gets no menu.
 */
export function useAgentListActions(org: string, canWrite: boolean): { agentActions: (a: Agent) => RowAction[]; tokenActions: (tok: AgentToken) => RowAction[] } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const revoke = useRevokeAgent(org);
  const withdraw = useWithdrawAgentToken(org);

  async function onRevoke(a: Agent) {
    if (!(await ask({ message: t.agents.revokeConfirm(a.host_name), confirmLabel: t.agents.revoke, destructive: true }))) return;
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

  return {
    agentActions: (a) => {
      const items: RowAction[] = [
        { id: 'open', label: t.agents.openHost, onSelect: () => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: a.host_id }, hash: 'connection' }) },
      ];
      if (canWrite) items.push({ id: 'revoke', label: t.agents.revokeItem, destructive: true, onSelect: () => void onRevoke(a) });
      return items;
    },
    tokenActions: (tok) => (canWrite ? [{ id: 'withdraw', label: t.agents.withdraw, onSelect: () => void onWithdraw(tok) }] : []),
  };
}
