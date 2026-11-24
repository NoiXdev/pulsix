import { useEffect, useRef, type RefObject } from 'react';

/**
 * useAskOnEscape keeps a dialog with unsaved changes open on Escape and
 * asks instead, wherever the focus is — in the dialog's content or fallen
 * to `<body>` after a click on its title bar, padding or text (backlog
 * 3.121). `inside` is any element in the dialog's content; it names the
 * dialog the hook speaks for.
 *
 * While `open`, it listens for the keydown on `document`. With
 * `shouldAsk`, it prevents the keydown's default — the one thing that stops
 * the native <dialog>'s close request — and calls `ask`. The close request
 * itself (the dialog's `cancel`) must never be what asks: answering the
 * question with Escape spends the browser's user activation, after which
 * the next `cancel` is not cancelable and would close the dialog with the
 * edits.
 *
 * It listens in the bubble phase, after the app's own handlers, so an
 * Escape a control inside has already taken (a picker closing its list, a
 * code editor releasing Tab) is left to that control. An Escape while
 * another dialog is open — a nested one, or the question itself — is left to
 * that dialog.
 */
export function useAskOnEscape(inside: RefObject<Element | null>, open: boolean, shouldAsk: boolean, ask: () => void) {
  const latest = useRef({ shouldAsk, ask });
  useEffect(() => {
    latest.current = { shouldAsk, ask };
  });

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || !latest.current.shouldAsk) return;
      const own = inside.current?.closest('dialog');
      if (!own?.open || otherDialogOpen(own)) return;
      e.preventDefault();
      latest.current.ask();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open, inside]);
}

/** Whether a dialog other than `own` and those around it is open: one
 * opened from it, the shared question, or any other. The DOM does not say
 * which is on top, so any of them leaves the press alone; the dialog's own
 * `onCancel` is still there as the fallback. */
function otherDialogOpen(own: HTMLDialogElement): boolean {
  for (const d of document.querySelectorAll('dialog[open]')) {
    if (d !== own && !d.contains(own)) return true;
  }
  return false;
}
