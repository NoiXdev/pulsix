import { createContext, useContext } from 'react';

export interface ConfirmOptions {
  message: string;
  /** Defaults to the catalogue's "Are you sure?". */
  title?: string;
  /** Defaults to the catalogue's "Confirm". */
  confirmLabel?: string;
  /** Shows the confirm button in the destructive variant (delete, revoke,
   * remove). A neutral confirmation leaves it off and gets the primary one. */
  destructive?: boolean;
  /** Withdraws the question when aborted: it resolves `false`, and its
   * dialog closes (or, still queued, is never shown). */
  signal?: AbortSignal;
}

export type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>;

export const ConfirmContext = createContext<ConfirmFn | null>(null);

/** useConfirm returns `confirm(options)`, which asks in a Pulsix dialog
 * and resolves `true` on Confirm and `false` on Cancel, Escape, an aborted
 * `signal` or any other dismissal. Split from confirm.tsx so that file exports only the component
 * (react-refresh/only-export-components). */
export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm must be used within a ConfirmProvider');
  return ctx;
}
