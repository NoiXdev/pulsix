import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';

/** VersionBanner is what the dialog collapses into on "Later": a strip in
 * the shell's normal flow between the header and the page, so it takes its own
 * space and never covers page controls. It stays until reload. */
export function VersionBanner({ version, onReload }: { version: string; onReload: () => void }) {
  const t = useT();
  return (
    <div
      role="status"
      className="flex shrink-0 items-center justify-center gap-3 border-b border-border bg-card px-4 py-2 text-sm text-foreground"
    >
      <span>{t.version.bannerText(version)}</span>
      <Button type="button" variant="primary" onClick={onReload}>
        {t.version.reload}
      </Button>
    </div>
  );
}
