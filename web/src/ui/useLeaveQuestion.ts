import { useCallback, useRef } from 'react';
import { useRouter } from '@tanstack/react-router';

type HistoryAction = 'PUSH' | 'REPLACE' | 'BACK' | 'FORWARD' | 'GO';

const POPS: readonly HistoryAction[] = ['BACK', 'FORWARD', 'GO'];

interface Question {
  pop: boolean;
  /** How far the browser moved for this pop, relative to the entry the
   * router is on; undefined for a push or when the entries carry no index. */
  delta: number | undefined;
  superseded: boolean;
  dismissed: boolean;
  controller: AbortController;
}

/** The position @tanstack/history stores in every entry it creates. */
function entryIndex(state: unknown): number | undefined {
  const index = (state as { __TSR_index?: unknown } | null | undefined)?.__TSR_index;
  return typeof index === 'number' ? index : undefined;
}

/**
 * useLeaveQuestion turns "may the user leave?" into a router blocker
 * function that also survives a second navigation while the question is open
 * (the in-page dialog does not freeze the page as a native confirm did).
 *
 * - While a question is open, every further navigation is blocked at once
 *   and the first question stays the only one.
 * - A history pop (browser Back/Forward) needs care. A pop is already done
 *   when the blocker runs, and @tanstack/history only undoes it after the
 *   answer, with `history.go(-delta)`. A second pop moves the browser again
 *   and its own undo moves it back, so by the time the first question is
 *   answered the browser is no longer where that undo expects it: Cancel
 *   would step one entry too far (or, when there is no entry there, leave
 *   history ignoring the next real pop). So once superseded the first pop is
 *   let through ("false": the browser is already back on this page, the
 *   router just syncs to it), and a Confirm then moves the browser itself,
 *   by the distance captured when the first pop arrived.
 * - A pop that lands on the entry the router is already on (Back, then
 *   Forward while the question is open) is never blocked: its undo would be
 *   `history.go(0)`, which reloads the page. It is let through, and only
 *   marks an open question superseded.
 * - `dismiss` answers an open question from outside — a navigation that
 *   bypasses the blocker (a successful save leaving the form) makes it moot.
 *   Its dialog closes (`ask` gets an aborted signal), a dismissed pop is let
 *   through without an undo and a dismissed push is dropped, so the
 *   bypassing navigation is the one that happens.
 *
 * `ask` resolves true when the user agrees to leave. `shouldBlock` resolves
 * true when the navigation is to be blocked; it must be called synchronously
 * from the router's blocker, before anything is awaited, because it reads
 * where the browser is.
 */
export function useLeaveQuestion(ask: (signal: AbortSignal) => Promise<boolean>) {
  const router = useRouter();
  const open = useRef<Question | null>(null);

  const shouldBlock = async ({ action }: { action: HistoryAction }): Promise<boolean> => {
    const pop = POPS.includes(action);
    let delta: number | undefined;
    if (pop) {
      const to = entryIndex(window.history.state);
      const from = entryIndex(router.history.location.state);
      if (to !== undefined && from !== undefined) delta = to - from;
    }
    if (delta === 0) {
      if (open.current) open.current.superseded = true;
      return false;
    }
    if (open.current) {
      if (pop) open.current.superseded = true;
      return true;
    }
    const question: Question = { pop, delta, superseded: false, dismissed: false, controller: new AbortController() };
    open.current = question;
    try {
      const leave = await ask(question.controller.signal);
      if (question.dismissed) return !question.pop;
      if (question.pop && question.superseded) {
        if (leave) {
          const distance = question.delta ?? (action === 'BACK' ? -1 : action === 'FORWARD' ? 1 : undefined);
          if (distance !== undefined) router.history.go(distance, { ignoreBlocker: true });
        }
        return false;
      }
      return !leave;
    } finally {
      open.current = null;
    }
  };

  const dismiss = useCallback(() => {
    const question = open.current;
    if (!question) return;
    question.dismissed = true;
    question.controller.abort();
  }, []);

  return { shouldBlock, dismiss };
}
