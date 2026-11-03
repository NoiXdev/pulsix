import { X } from 'lucide-react';
import { useHost } from '../../api/hooks/useHosts.ts';
import { useT } from '../../i18n/useT.ts';
import { shortId } from './status.ts';

/**
 * HostFilterChip names the host the runs list is narrowed to and removes the
 * filter on click. The label falls back to the short id while the host loads
 * (or when it cannot be read), so the filter is never invisible.
 */
export function HostFilterChip({ org, hostId, onRemove }: { org: string; hostId: string; onRemove: () => void }) {
  const t = useT();
  const host = useHost(org, hostId);
  const name = host.data?.host.name ?? shortId(hostId);
  return (
    <div>
      <span className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-3 py-1 text-xs">
        {t.runs.list.hostFilter(name)}
        <button
          type="button"
          aria-label={t.runs.list.removeHostFilter}
          onClick={onRemove}
          className="text-muted-foreground hover:text-foreground"
        >
          <X size={13} />
        </button>
      </span>
    </div>
  );
}
