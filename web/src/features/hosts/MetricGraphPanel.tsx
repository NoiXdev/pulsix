/**
 * The graph behind a metric row's chart button. Split out of
 * HostChecksCard.tsx under backlog 3.42: it owns the lazy chart and the
 * Suspense/ErrorBoundary shell around it, machinery nothing else on the
 * card uses.
 */
import { lazy, Suspense } from 'react';
import { Button } from '../../ui/button.tsx';
import { Select, type SelectOption } from '../../ui/Select.tsx';
import { ErrorBoundary } from '../../ui/ErrorBoundary.tsx';
import { useMonitoringSettings } from '../../api/hooks/useMonitoringSettings.ts';
import { useHostMetric, type MetricSelection } from '../../api/hooks/useChecks.ts';
import { importMetricChart } from './metricChartImport.ts';
import type { MetricRange } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

const MetricChart = lazy(() => importMetricChart().then((m) => ({ default: m.MetricChart })));

const RANGES: MetricRange[] = ['1h', '24h', '7d'];

/** The longer ranges (backlog 3.15) and the days of retention each needs:
 * a window beyond what the organization keeps would only show empty space. */
const LONG_RANGES: Array<[MetricRange, number]> = [
  ['30d', 30],
  ['90d', 90],
];

/** The chart's own height, shared with the Suspense fallback so a cold
 * open does not shift the panel and everything under it by ~164 px while
 * the chunk downloads. */
const CHART_HEIGHT = 180;

export function MetricGraphPanel({
  org,
  hostId,
  label,
  selection,
  vantages,
  onRange,
}: {
  org: string;
  hostId: string;
  label: string;
  selection: MetricSelection;
  /** The series a check probed from several places offers (backlog
   * 3.13a): the combined one (value '') and each probing point's own.
   * Absent for any other check, which has only its own series. */
  vantages?: SelectOption[];
  onRange: (selection: MetricSelection) => void;
}) {
  const t = useT();
  // Read by every member; while it loads, or if it fails, only the
  // always-available ranges are offered.
  const retention = useMonitoringSettings(org).data?.settings.retention_effective_days ?? 0;
  const ranges = [...RANGES, ...LONG_RANGES.filter(([, days]) => retention >= days).map(([range]) => range)];
  // A long range stops being offered when the retention falls below it; the
  // panel then shows (and asks for) 7 d rather than a window no button names.
  const active: MetricSelection = ranges.includes(selection.range) ? selection : { ...selection, range: '7d' };
  const series = useHostMetric(org, hostId, active);

  return (
    <div className="rounded-md border border-border p-2">
      <div className="mb-2 flex flex-wrap items-center gap-1">
        {ranges.map((value) => (
          <Button
            key={value}
            type="button"
            variant={value === active.range ? 'primary' : 'ghost'}
            aria-pressed={value === active.range}
            onClick={() => onRange({ ...active, range: value })}
          >
            {t.hosts.graph.ranges[value]}
          </Button>
        ))}
        {vantages && (
          <div className="ml-auto w-48">
            <Select
              aria-label={t.hosts.vantages.graphVantage}
              options={vantages}
              value={active.vantage ?? ''}
              onChange={(v) => {
                const combined: MetricSelection = { check: active.check, item: active.item, metric: active.metric, range: active.range };
                onRange(v === '' ? combined : { ...combined, vantage: v });
              }}
            />
          </div>
        )}
      </div>
      {series.isError && (
        <p role="alert" className="text-xs text-destructive">
          {series.error instanceof Error ? series.error.message : t.hosts.graph.readFailed}
        </p>
      )}
      {!series.data && !series.isError && <p className="text-xs text-muted-foreground">{t.common.loading}</p>}
      {series.data && (
        // The boundary wraps the Suspense, not the other way round: a
        // rejected chunk import is re-thrown during render, and without
        // this the throw reaches the router and takes the whole host page
        // with it. Scoped here it costs the graph and nothing else.
        <ErrorBoundary
          // Stable by value, and it changes exactly when the operator asks
          // for a different series or window — so a chart that throws on
          // one input does not wedge the panel for the next.
          resetKey={`${selection.check}:${selection.item}:${selection.metric}:${active.range}:${active.vantage ?? ''}`}
          fallback={
            <p role="alert" className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: CHART_HEIGHT }}>
              {t.hosts.graph.chartFailed}
            </p>
          }
        >
          <Suspense
            fallback={
              <p className="flex items-center justify-center text-xs text-muted-foreground" style={{ height: CHART_HEIGHT }}>
                {t.common.loading}
              </p>
            }
          >
            <MetricChart points={series.data.points ?? []} range={active.range} label={label} height={CHART_HEIGHT} />
          </Suspense>
        </ErrorBoundary>
      )}
    </div>
  );
}
