import { useCallback, useEffect, useRef } from 'react';
import { useBlocker } from '@tanstack/react-router';
import { useT } from '../i18n/useT.ts';
import { useConfirm } from './confirmContext.ts';

/**
 * useUnsavedGuard asks before a page with unsaved changes is left — a link,
 * a nav item, the browser's back button — through the router's blocker,
 * and lets a closing or reloading tab get the browser's own prompt. The
 * returned `release` switches it off for good, synchronously, for the
 * navigation a successful save triggers itself (FormPage).
 *
 * Refs, so the blocker always sees the latest state without re-registering
 * and `release` takes effect before React renders the page as clean.
 */
export function useUnsavedGuard(dirty: boolean): () => void {
  const t = useT();
  const ask = useConfirm();
  const dirtyRef = useRef(dirty);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  const released = useRef(false);
  // The question that is open. The in-page dialog does not freeze the page
  // as a native confirm did, so a second navigation can arrive meanwhile:
  // it is blocked at once and the first question stays the only one.
  const asking = useRef(false);

  useBlocker({
    shouldBlockFn: async () => {
      if (released.current || !dirtyRef.current) return false;
      if (asking.current) return true;
      asking.current = true;
      try {
        return !(await ask({ message: t.ui.formPage.unsaved }));
      } finally {
        asking.current = false;
      }
    },
    enableBeforeUnload: () => dirtyRef.current && !released.current,
  });

  return useCallback(() => {
    released.current = true;
  }, []);
}
