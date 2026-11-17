import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';
import { mmss, useCountdown } from './useCountdown.ts';

/**
 * RenewBanner is what RenewDialog collapses into on "Later" or Escape: a
 * non-modal strip at the top with the countdown and a way back to the
 * dialog, so the page stays usable while the end approaches. It stays until
 * the session is renewed or ends.
 */
export function RenewBanner({ expiresAtMs, onRenew }: { expiresAtMs: number; onRenew: () => void }) {
  const t = useT();
  const left = useCountdown(expiresAtMs);
  return (
    <div
      role="status"
      className="fixed inset-x-0 top-0 z-50 flex items-center justify-center gap-3 border-b border-border bg-card px-4 py-2 text-sm text-foreground"
    >
      <span>
        {t.session.bannerText} <span className="font-mono">{mmss(left)}</span>
      </span>
      <Button type="button" variant="primary" onClick={onRenew}>
        {t.session.bannerRenew}
      </Button>
    </div>
  );
}
