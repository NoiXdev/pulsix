import { useCallback, useEffect, useRef } from 'react';
import { useBlocker } from '@tanstack/react-router';
import { useT } from '../i18n/useT.ts';
import { useConfirm } from './confirmContext.ts';
import { useLeaveQuestion } from './useLeaveQuestion.ts';

/**
 * useUnsavedGuard asks before a page with unsaved changes is left — a link,
 * a nav item, the browser's back button — through the router's blocker,
 * and lets a closing or reloading tab get the browser's own prompt. The
 * returned `release` switches it off for good, synchronously, for the
 * navigation a successful save triggers itself (FormPage), and dismisses a
 * leave question still open from before the save finished: that navigation
 * bypasses it, so its answer no longer matters. Switching off is immediate;
 * the promise it returns resolves once the router has acted on a dismissed
 * question, and the save's navigation waits for it (backlog 1.52).
 *
 * Refs, so the blocker always sees the latest state without re-registering
 * and `release` takes effect before React renders the page as clean.
 *
 * `samePath` lets a navigation that keeps the pathname through without a
 * question: a page whose tabs live in the search (`?tab=`) and keep the
 * unsaved form mounted is not left by a tab switch.
 */
export function useUnsavedGuard(dirty: boolean, { samePath = false }: { samePath?: boolean } = {}): () => Promise<void> {
  const t = useT();
  const ask = useConfirm();
  const dirtyRef = useRef(dirty);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  const released = useRef(false);
  const leave = useLeaveQuestion((signal) => ask({ message: t.ui.formPage.unsaved, signal }));
  const { dismiss } = leave;

  useBlocker({
    shouldBlockFn: async (args) => {
      if (released.current || !dirtyRef.current) return false;
      if (samePath && args.next.pathname === args.current.pathname) return false;
      return leave.shouldBlock(args);
    },
    enableBeforeUnload: () => dirtyRef.current && !released.current,
  });

  return useCallback(() => {
    released.current = true;
    return dismiss();
  }, [dismiss]);
}
