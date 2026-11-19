import { Pill } from '../../ui/badge.tsx';
import type { Repo } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/** The last sync outcome: crit when the checkout failed (the error rides as
 * the title), ok when it succeeded, warn while it never ran. */
export function SyncState({ repo: r }: { repo: Repo }) {
  const t = useT();
  if (r.last_sync_error) {
    return (
      <span className="relative z-10" title={r.last_sync_error}>
        <Pill tone="crit">{t.repos.list.syncFailed}</Pill>
      </span>
    );
  }
  if (r.last_sync) return <Pill tone="ok">{t.repos.list.synced(r.last_sync.slice(0, 10))}</Pill>;
  return <Pill tone="warn">{t.repos.list.neverSynced}</Pill>;
}
