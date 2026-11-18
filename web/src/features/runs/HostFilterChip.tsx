import { useHost } from '../../api/hooks/useHosts.ts';
import { useT } from '../../i18n/useT.ts';
import { ActiveFilter } from '../../ui/list-toolbar.tsx';
import { shortId } from './status.ts';

/**
 * HostFilterChip names the host a list (runs, alerts) is narrowed to, as a
 * removable filter pill. The label falls back to the short id while the
 * host loads (or when it cannot be read), so the filter is never invisible.
 */
export function HostFilterChip({ org, hostId, onRemove }: { org: string; hostId: string; onRemove: () => void }) {
  const t = useT();
  const host = useHost(org, hostId);
  const name = host.data?.host.name ?? shortId(hostId);
  return <ActiveFilter label={t.runs.list.hostFilter(name)} removeLabel={t.runs.list.removeHostFilter} onRemove={onRemove} />;
}
