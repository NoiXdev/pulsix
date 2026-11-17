import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';

/** VersionBanner is what the dialog collapses into on "Later": a strip at the
 * bottom edge (the expiry banner owns the top) that stays until reload. */
export function VersionBanner({ version, onReload }: { version: string; onReload: () => void }) {
  const t = useT();
  return (
    <div
      role="status"
      className="fixed inset-x-0 bottom-0 z-40 flex items-center justify-center gap-3 border-t border-border bg-card px-4 py-2 text-sm text-foreground"
    >
      <span>{t.version.bannerText(version)}</span>
      <Button type="button" variant="primary" onClick={onReload}>
        {t.version.reload}
      </Button>
    </div>
  );
}
