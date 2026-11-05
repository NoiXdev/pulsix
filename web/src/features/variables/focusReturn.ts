import { useEffect, useRef } from 'react';

/**
 * useFocusReturn gives the focus back to the element that opened a dialog
 * once the dialog is gone. The field dialog is unmounted while it is open,
 * not hidden, so the browser's own return to the opener does not happen.
 *
 * `open` says whether the dialog is mounted. The returned function is
 * called with the opener when the dialog opens (`e.currentTarget` of the
 * click). When the opener has left the page meanwhile (its field was
 * deleted), `fallback` names the element to focus instead.
 */
export function useFocusReturn(open: boolean, fallback?: () => HTMLElement | null): (opener: HTMLElement) => void {
  const opener = useRef<HTMLElement | null>(null);
  const wasOpen = useRef(false);
  const fallbackRef = useRef(fallback);
  useEffect(() => {
    fallbackRef.current = fallback;
    if (wasOpen.current && !open) {
      const target = opener.current?.isConnected ? opener.current : fallbackRef.current?.();
      target?.focus();
      opener.current = null;
    }
    wasOpen.current = open;
  });
  return (el) => {
    opener.current = el;
  };
}
