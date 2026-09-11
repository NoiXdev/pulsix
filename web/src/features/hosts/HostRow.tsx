import { Link } from '@tanstack/react-router';
import { MoreHorizontal } from 'lucide-react';
import { GridRow } from '../../ui/table.tsx';
import { Tag } from '../../ui/badge.tsx';
import { CapabilityCell } from './capabilities.tsx';
import { useHostTags } from '../../api/hooks/useTags.ts';
import { useSetHostCapabilities } from '../../api/hooks/useHosts.ts';
import type { Host } from '../../api/types.ts';

function timeAgo(iso?: string | null): string {
  if (!iso) return '—';
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h`;
  return `${Math.floor(seconds / 86400)} d`;
}

export function HostRow({
  org,
  host,
  columns,
  zoneName,
}: {
  org: string;
  host: Host;
  columns: string;
  /** The host's zone slug, resolved by the caller (HostsListPage, which
   * already loads the org's zones for the "Add host" dialog) — cheap fix,
   * task-15a-report.md fix round 1: this used to show a raw UUID prefix. */
  zoneName?: string;
}) {
  const tags = useHostTags(org, host.id);
  const setCapabilities = useSetHostCapabilities(org, host.id);

  return (
    <GridRow columns={columns}>
      {/* Always "unknown": there is no monitoring backend yet in v0.1, so
         last_seen_at cannot honestly claim "ok" — cheap fix. */}
      <span className="h-2 w-2 rounded-full" style={{ background: 'var(--px-unknown)' }} title="unknown" />
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: host.id }}
        className="truncate font-mono text-xs font-semibold text-foreground hover:text-brand"
      >
        {host.name}
      </Link>
      <span className="truncate font-mono text-xs text-muted-foreground">{zoneName ?? host.zone_id.slice(0, 8)}</span>
      <div className="flex flex-wrap gap-1">
        {(tags.data?.tags ?? []).map((t) => (
          <Tag key={t.key}>
            {t.key}={t.value}
          </Tag>
        ))}
      </div>
      <CapabilityCell
        kind="managed"
        on={host.managed}
        onChange={(patch) => setCapabilities.mutate(patch)}
      />
      <CapabilityCell
        kind="monitored"
        on={host.monitored}
        onChange={(patch) => setCapabilities.mutate(patch)}
      />
      <span className="font-mono text-[11px] text-muted-foreground">{timeAgo(host.last_seen_at)}</span>
      <MoreHorizontal size={15} className="text-muted-foreground" />
    </GridRow>
  );
}
