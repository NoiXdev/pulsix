import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useT } from '../../i18n/useT.ts';
import { releaseNotesUrl } from './releaseNotes.ts';

/** VersionDialog tells the person the manager was upgraded under their open
 * tab. Escape counts as "Later". */
export function VersionDialog({ version, onReload, onLater }: { version: string; onReload: () => void; onLater: () => void }) {
  const t = useT();
  const url = releaseNotesUrl(version);
  return (
    <Dialog open onClose={onLater} title={t.version.title(version)}>
      <p className="text-sm text-muted-foreground">{t.version.intro}</p>
      {url && (
        <p className="mt-3 text-sm">
          <a href={url} target="_blank" rel="noopener noreferrer" className="underline">
            {t.version.releaseNotes}
            <span className="sr-only"> {t.shell.opensInNewTab}</span>
          </a>
        </p>
      )}
      <div className="mt-4 flex justify-end gap-2">
        <Button type="button" onClick={onLater}>
          {t.version.later}
        </Button>
        <Button type="button" variant="primary" onClick={onReload}>
          {t.version.reload}
        </Button>
      </div>
    </Dialog>
  );
}
