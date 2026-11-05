import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * StaleNotice is what a host write that answered 412 shows: the host
 * changed since this page loaded it, so the edit was not applied. Reload
 * refetches the host and what hangs off it (`onReload`); a reload that
 * throws says so in place. The caller removes the notice once the reload
 * resolved, since only it knows what a fresh host means for its own form.
 */
export function StaleNotice({ onReload }: { onReload: () => Promise<unknown> }) {
  const t = useT();
  const [reloading, setReloading] = useState(false);
  const [failed, setFailed] = useState(false);

  async function reload() {
    setReloading(true);
    setFailed(false);
    try {
      await onReload();
    } catch {
      setFailed(true);
    } finally {
      setReloading(false);
    }
  }

  return (
    <div role="alert" className="flex flex-col gap-1.5 rounded-md border border-border bg-background p-3 text-xs text-destructive">
      <div className="flex items-center justify-between gap-3">
        <span>{t.hosts.dialog.stale}</span>
        <Button type="button" onClick={() => void reload()} disabled={reloading}>
          {t.hosts.dialog.reload}
        </Button>
      </div>
      {failed && <span>{t.hosts.dialog.reloadFailed}</span>}
    </div>
  );
}
