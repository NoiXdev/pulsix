import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { ExpiryNoticeContext, useExpiryNotice, type ExpiryBannerState } from './expiryNotice.ts';
import { RenewBanner } from './RenewBanner.tsx';

/** ExpiryNoticeProvider holds the expiry banner between ExpiryWatcher and
 * the place that draws it (see expiryNotice.ts). */
export function ExpiryNoticeProvider({ children }: { children: ReactNode }) {
  const [banner, setBanner] = useState<ExpiryBannerState | null>(null);
  const [slots, setSlots] = useState(0);
  const addSlot = useCallback(() => {
    setSlots((n) => n + 1);
    return () => setSlots((n) => n - 1);
  }, []);
  const value = useMemo(() => ({ banner, setBanner, slots, addSlot }), [banner, slots, addSlot]);
  return <ExpiryNoticeContext.Provider value={value}>{children}</ExpiryNoticeContext.Provider>;
}

/** ExpiryNotice is the banner's place inside a page shell: OrgLayout and
 * AccountSecurityLayout pass it in AppShell's notice slot. */
export function ExpiryNotice() {
  const notice = useExpiryNotice();
  const addSlot = notice?.addSlot;
  useEffect(() => addSlot?.(), [addSlot]);
  if (!notice?.banner) return null;
  return <RenewBanner expiresAtMs={notice.banner.expiresAtMs} onRenew={notice.banner.onRenew} />;
}

/** ExpiryNoticeFallback draws the banner at the top of a page without a
 * shell (the lock-PIN prompt, say), where no ExpiryNotice is mounted.
 * RootLayout renders it above the page. */
export function ExpiryNoticeFallback() {
  const notice = useExpiryNotice();
  if (!notice?.banner || notice.slots > 0) return null;
  return <RenewBanner expiresAtMs={notice.banner.expiresAtMs} onRenew={notice.banner.onRenew} />;
}
