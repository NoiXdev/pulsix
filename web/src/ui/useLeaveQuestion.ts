import { useRef } from 'react';
import { useRouter } from '@tanstack/react-router';

type HistoryAction = 'PUSH' | 'REPLACE' | 'BACK' | 'FORWARD' | 'GO';

const POPS: readonly HistoryAction[] = ['BACK', 'FORWARD', 'GO'];

/**
 * useLeaveQuestion turns "may the user leave?" into a router blocker
 * function that also survives a second navigation while the question is open
 * (the in-page dialog does not freeze the page as a native confirm did).
 *
 * - While a question is open, every further navigation is blocked at once
 *   and the first question stays the only one.
 * - A second history pop (browser Back) needs care. A pop is already done
 *   when the blocker runs, and @tanstack/history only undoes it after the
 *   answer, with `history.go(-delta)`. The second pop moves the browser
 *   again and its own restore moves it back, so by the time the first
 *   question is answered the browser is no longer where that undo expects
 *   it: Cancel would step one entry too far (or, when there is no entry
 *   there, leave history ignoring the next real pop). So once superseded the
 *   first pop is let through ("false": the browser is already back on this
 *   page, the router just syncs to it), and a Confirm then goes Back itself.
 *
 * `ask` resolves true when the user agrees to leave. The returned function
 * resolves true when the navigation is to be blocked.
 */
export function useLeaveQuestion(ask: () => Promise<boolean>) {
  const router = useRouter();
  const open = useRef<{ pop: boolean; superseded: boolean } | null>(null);

  return async ({ action }: { action: HistoryAction }): Promise<boolean> => {
    const pop = POPS.includes(action);
    if (open.current) {
      if (pop) open.current.superseded = true;
      return true;
    }
    const question = { pop, superseded: false };
    open.current = question;
    try {
      const leave = await ask();
      if (question.pop && question.superseded) {
        // Only a Back can be repeated faithfully; a Forward or a Go
        // (an arbitrary distance) stays on the page.
        if (leave && action === 'BACK') router.history.back({ ignoreBlocker: true });
        return false;
      }
      return !leave;
    } finally {
      open.current = null;
    }
  };
}
