import { useCallback, useEffect, useRef } from 'react';
import { useBlocker } from '@tanstack/react-router';
import { useT } from '../i18n/useT.ts';

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
  const dirtyRef = useRef(dirty);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  const released = useRef(false);

  useBlocker({
    shouldBlockFn: () => {
      if (released.current || !dirtyRef.current) return false;
      return !window.confirm(t.ui.formPage.unsaved);
    },
    enableBeforeUnload: () => dirtyRef.current && !released.current,
  });

  return useCallback(() => {
    released.current = true;
  }, []);
}
