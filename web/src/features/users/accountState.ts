import type { Tone } from '../../ui/badge.tsx';
import type { AccountState } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { ActionKind } from './useAccountActionCalls.ts';

/** The pill tone of each account state: green for an account in use, warn
 * for one that still needs something from its person, grey for one that is
 * switched off, accent for an invitation nobody accepted yet. */
const TONE: Record<AccountState, Tone> = {
  active: 'ok',
  must_change_password: 'warn',
  no_second_factor: 'warn',
  deactivated: 'unknown',
  invite_pending: 'accent',
};

export function accountStateTone(state: AccountState): Tone {
  return TONE[state];
}

export function accountStateLabel(t: Messages, state: AccountState): string {
  return t.users.state[state];
}

/** Which actions an account offers. An account whose invitation was never
 * accepted has no password, so nothing may hand it one: Deactivate is its
 * only action, and Reactivate once it is deactivated (its state then reads
 * deactivated, so `invitePending` says it). A deactivated account offers
 * Reactivate in Deactivate's place. */
export function availableActions(state: AccountState, invitePending: boolean): ActionKind[] {
  const toggle: ActionKind = state === 'deactivated' ? 'reactivate' : 'deactivate';
  if (invitePending || state === 'invite_pending') return [toggle];
  return ['resetLink', 'tempPassword', 'resetFactor', 'endSessions', toggle];
}
