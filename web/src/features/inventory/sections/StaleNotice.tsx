import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { useT } from '../../../i18n/useT.ts';

/** StaleNotice answers a 412: someone else saved this group first. Reload
 * runs `onReload` (refetch, then the caller starts its form over from the
 * stored group and removes the notice); a reload that fails says so in
 * place. The button is `type="button"`: the notice sits inside forms, and
 * a default button there would submit the stale form again. */
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
    <div role="alert" className="flex flex-col gap-1.5 rounded-md border border-border bg-card px-4 py-3 text-xs">
      <div className="flex items-center justify-between gap-3">
        <span>{t.inventory.stale.message}</span>
        <Button type="button" onClick={() => void reload()} disabled={reloading}>
          {t.inventory.stale.reload}
        </Button>
      </div>
      {failed && <span className="text-destructive">{t.inventory.stale.reloadFailed}</span>}
    </div>
  );
}
