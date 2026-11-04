import {
  useDeactivateUser,
  useEndSessions,
  useReactivateUser,
  useResetSecondFactor,
  useSendResetLink,
  useSetTemporaryPassword,
} from '../../api/hooks/useAdminUsers.ts';
import type { AccountNotice } from '../../api/types.ts';

export type ActionKind = 'resetLink' | 'tempPassword' | 'resetFactor' | 'endSessions' | 'deactivate' | 'reactivate';

/** What an action answered: how the notice mail went, if it sends one, and
 * for the temporary password the password itself. */
export interface ActionResult {
  notice?: AccountNotice;
  temporary?: { password: string; expires_at: string; notice: AccountNotice };
}

/** useAccountActionCalls gives each account action as one call with one
 * result shape, so the confirm dialog runs them alike. */
export function useAccountActionCalls(userId: string): Record<ActionKind, () => Promise<ActionResult>> {
  const resetLink = useSendResetLink(userId);
  const tempPassword = useSetTemporaryPassword(userId);
  const resetFactor = useResetSecondFactor(userId);
  const endSessions = useEndSessions(userId);
  const deactivate = useDeactivateUser(userId);
  const reactivate = useReactivateUser(userId);
  return {
    resetLink: async () => ({ notice: await resetLink.mutateAsync() }),
    tempPassword: async () => {
      const temporary = await tempPassword.mutateAsync();
      return { notice: temporary.notice, temporary };
    },
    resetFactor: async () => ({ notice: await resetFactor.mutateAsync() }),
    endSessions: async () => {
      await endSessions.mutateAsync();
      return {};
    },
    deactivate: async () => ({ notice: await deactivate.mutateAsync() }),
    reactivate: async () => {
      await reactivate.mutateAsync();
      return {};
    },
  };
}
