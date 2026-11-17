import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useRevokeAgent } from '../../api/hooks/useAgents.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * useRevokeAgentCommand is the one confirm-and-revoke of an agent, shared by
 * the Agents list's row menu and the host page's agent card. The two differ
 * only in what they say on success, so the caller passes that message.
 */
export function useRevokeAgentCommand(org: string): (agent: { host_id: string; host_name: string }, successMessage: string) => Promise<void> {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const revoke = useRevokeAgent(org);
  return async (agent, successMessage) => {
    if (!(await ask({ message: t.agents.revokeConfirm(agent.host_name), confirmLabel: t.agents.revoke, destructive: true }))) return;
    try {
      await revoke.mutateAsync(agent.host_id);
      toast.show(successMessage);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.agents.revokeFailed, 'destructive');
    }
  };
}
