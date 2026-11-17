import { createContext, useContext } from 'react';

/** What the collapsed expiry warning shows: the end it counts down to and
 * the way back to the renewal dialog. */
export interface ExpiryBannerState {
  expiresAtMs: number;
  onRenew: () => void;
}

export interface ExpiryNoticeValue {
  /** The banner to draw, or null while there is none. */
  banner: ExpiryBannerState | null;
  setBanner: (banner: ExpiryBannerState | null) => void;
  /** How many in-shell places (ExpiryNotice) are mounted. */
  slots: number;
  /** addSlot registers an in-shell place; the returned function removes it. */
  addSlot: () => () => void;
}

/**
 * ExpiryNoticeContext carries the expiry banner from ExpiryWatcher (mounted
 * by routes/RootLayout.tsx, above every page) to where it is drawn: the page
 * shell's notice slot, in the normal flow below the header, so it never
 * covers the shell's controls (backlog 3.94). Provided by
 * ExpiryNoticeProvider; without one (a test rendering the watcher alone)
 * the watcher draws the banner itself.
 */
export const ExpiryNoticeContext = createContext<ExpiryNoticeValue | null>(null);

export function useExpiryNotice(): ExpiryNoticeValue | null {
  return useContext(ExpiryNoticeContext);
}
