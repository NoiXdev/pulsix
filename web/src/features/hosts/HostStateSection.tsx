import { useHostChecks, useHostMetric } from '../../api/hooks/useChecks.ts';
import { Sparkline } from '../../ui/Sparkline.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { stateTiles, type StateTile, type TileTone } from './hostOverview.ts';

// The sparklines cover 24 h: a point more every 30 s changes nothing a
// reader can see, and a host page with four tiles asks four series each time.
const SPARKLINE_REFETCH_MS = 60_000;

const TONE: Record<TileTone, string> = {
  ok: 'var(--px-accent)',
  warn: 'var(--px-warn)',
  crit: 'var(--px-crit)',
  unknown: 'var(--px-unknown)',
};

/**
 * HostStateSection is the host page's "State" section: up to four tiles
 * (CPU, RAM, the fullest disk, load) with the current reading, a 24 h
 * sparkline and the colour of the reading's current verdict. It reads the
 * same host-checks query the checks section reads, and one 24 h series per
 * tile. Rendered only for a monitored host.
 */
export function HostStateSection({ org, hostId, canRead }: { org: string; hostId: string; canRead: boolean }) {
  const t = useT();
  return (
    <Section id="state" title={t.hosts.detail.section.state}>
      <StateTiles org={org} hostId={hostId} canRead={canRead} />
    </Section>
  );
}

function StateTiles({ org, hostId, canRead }: { org: string; hostId: string; canRead: boolean }) {
  const t = useT();
  const query = useHostChecks(org, hostId, canRead);

  if (!canRead) return <p className="text-xs text-muted-foreground">{t.hosts.checks.noAccess}</p>;
  if (query.isError) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {query.error instanceof Error ? query.error.message : t.hosts.checks.readFailed}
      </p>
    );
  }
  if (!query.data) return <p className="text-xs text-muted-foreground">{t.common.loading}</p>;

  const tiles = stateTiles(query.data.checks ?? []);
  if (tiles.length === 0) {
    return <p className="rounded-[10px] border border-border bg-card px-4 py-3 text-[13px] text-muted-foreground">{t.hosts.detail.tile.noReadings}</p>;
  }
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {tiles.map((tile) => (
        <Tile key={tile.kind} org={org} hostId={hostId} tile={tile} />
      ))}
    </div>
  );
}

function Tile({ org, hostId, tile }: { org: string; hostId: string; tile: StateTile }) {
  const t = useT();
  const format = useFormat();
  const series = useHostMetric(org, hostId, { check: tile.check, item: tile.item, metric: tile.metric, range: '24h' }, { refetchMs: SPARKLINE_REFETCH_MS });
  const percent = tile.kind !== 'load';
  const label =
    tile.kind === 'disk' ? t.hosts.detail.tile.disk(tile.item) : tile.kind === 'cpu' ? t.hosts.detail.tile.cpu : tile.kind === 'ram' ? t.hosts.detail.tile.ram : t.hosts.detail.tile.load;
  const value = percent ? `${format.number(tile.value, { maximumFractionDigits: 0 })} %` : format.number(tile.value, { maximumFractionDigits: 2 });
  const alarming = tile.tone === 'warn' || tile.tone === 'crit';

  return (
    <div data-tile data-tone={tile.tone} className="flex min-w-0 flex-col gap-1 rounded-[10px] border border-border bg-card px-3.5 py-3">
      {/* Not uppercased: a mount path is case-sensitive. It wraps rather
          than truncating, so the whole path is always readable. */}
      <span className="font-mono text-[11px] font-semibold break-all text-muted-foreground">
        {label}
      </span>
      <span className="font-mono text-lg font-bold" style={alarming ? { color: TONE[tile.tone] } : undefined}>
        {value}
      </span>
      <Sparkline
        values={(series.data?.points ?? []).map((p) => p.value)}
        domain={percent ? [0, 100] : undefined}
        label={t.hosts.detail.tile.last24h(label)}
        color={TONE[tile.tone]}
      />
    </div>
  );
}
