import { useEffect, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { refetchMe, useMe } from '../../api/hooks/useMe.ts';
import { resetSessionCache } from '../../lib/session.ts';
import { serverNow } from '../../lib/serverClock.ts';
import { onSessionMessage } from '../lock/lockChannel.ts';
import { markRenewed } from '../../lib/sessionEnd.ts';
import { RenewBanner } from './RenewBanner.tsx';
import { RenewDialog } from './RenewDialog.tsx';

/** How long before the session ends the warning appears. */
const WARN_MS = 5 * 60 * 1000;
/** The longest wait before looking at the clock again while the warning is
 * not due yet, so a clock jump (a laptop waking up) is noticed. */
const IDLE_CHECK_MS = 30 * 1000;

/**
 * ExpiryWatcher counts the session down (backlog 3.93). Five minutes before
 * `session_expires_at` it shows RenewDialog; at zero it drops the cached
 * data and sends the person to /login with a notice. RootLayout mounts it
 * only for an unlocked, MFA-complete session on a page that has one.
 *
 * Time is the server's: the expiry is a server timestamp, so the remaining
 * time is taken against the clock learned from /auth/me's Date header
 * (lib/serverClock.ts), not the browser's alone. /auth/me is read again when
 * the tab comes back to the foreground, which also corrects the offset, and
 * when another tab renews.
 */
export function ExpiryWatcher() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const me = useMe();
  const expiresAtMs = me.data ? Date.parse(me.data.session_expires_at) : NaN;
  const [warn, setWarn] = useState(false);
  // The expiry the person chose "Later" for: keyed, so a renewal (a new
  // expiry) brings the dialog back at the next warning without a reset.
  const [laterFor, setLaterFor] = useState<number | null>(null);
  const updatedAt = me.dataUpdatedAt;

  useEffect(() => {
    if (Number.isNaN(expiresAtMs)) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    async function expire() {
      // Another tab (or an earlier renewal) may have extended the session
      // behind this tab's back; the server has the last word.
      const fresh = await refetchMe(qc).catch(() => null);
      if (stopped) return;
      if (fresh && Date.parse(fresh.session_expires_at) - serverNow() > 0) return;
      // Navigate first, drop the cache after: see lib/session.ts.
      await navigate({ to: '/login', search: { step: undefined, notice: 'session_expired' } });
      resetSessionCache(qc);
    }

    function tick() {
      const remaining = expiresAtMs - serverNow();
      if (remaining <= 0) {
        void expire();
        return;
      }
      setWarn(remaining <= WARN_MS);
      timer = setTimeout(tick, remaining > WARN_MS ? Math.min(remaining - WARN_MS, IDLE_CHECK_MS) : remaining);
    }

    tick();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
    // updatedAt: a fresh /auth/me may carry the same expiry with a new
    // server clock offset, which moves the moments computed above.
  }, [expiresAtMs, updatedAt, qc, navigate]);

  useEffect(() => {
    const reread = () => {
      if (document.visibilityState === 'visible') void refetchMe(qc).catch(() => undefined);
    };
    document.addEventListener('visibilitychange', reread);
    window.addEventListener('focus', reread);
    // BroadcastChannel also delivers to other instances of this same tab, so
    // a renewal made here arrives here too; reading /auth/me again is
    // harmless.
    const stop = onSessionMessage((msg) => {
      if (msg.type === 'renewed') {
        markRenewed();
        void refetchMe(qc).catch(() => undefined);
      }
    });
    return () => {
      document.removeEventListener('visibilitychange', reread);
      window.removeEventListener('focus', reread);
      stop();
    };
  }, [qc]);

  if (!warn || Number.isNaN(expiresAtMs)) return null;
  if (laterFor === expiresAtMs) {
    return <RenewBanner expiresAtMs={expiresAtMs} onRenew={() => setLaterFor(null)} />;
  }
  return (
    <RenewDialog expiresAtMs={expiresAtMs} onRenewed={() => setWarn(false)} onLater={() => setLaterFor(expiresAtMs)} />
  );
}
