import { useEffect, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { MetricPoint, MetricRange } from '../api/types.ts';
import { toAlignedData } from './metricSeries.ts';

/** The window a chart covers, spelled out for the accessible name and the
 * x-axis tick format. Keyed by the same three values the metrics route
 * accepts. */
const RANGE_LABEL: Record<MetricRange, string> = {
  '1h': 'the last hour',
  '24h': 'the last 24 hours',
  '7d': 'the last 7 days',
};

/**
 * Tick formats per window, in uPlot's own template syntax: within an hour
 * or a day the date is noise and the clock is the information; across a
 * week the opposite. uPlot's default cascade would decide this per zoom
 * level, but these charts never zoom — the range switch is the zoom — so
 * pinning one format per range keeps the axis stable while data streams
 * in.
 */
const TICK_FORMAT: Record<MetricRange, string> = {
  '1h': '{HH}:{mm}',
  '24h': '{HH}:{mm}',
  '7d': '{DD}.{MM} {HH}h',
};

/** Fallbacks for the brand tokens below, mirroring src/styles/tokens.css.
 * They are only reached where the tokens cannot be resolved (jsdom's
 * getComputedStyle answers '' for a custom property), never in a browser. */
const FALLBACK = { accent: '#00E5FF', border: '#1E2336', muted: '#6B7280' };

/** token resolves a CSS custom property to a concrete colour. A canvas
 * cannot take `var(--px-accent)` — the 2D context needs a literal — so the
 * value is read off the document element at draw time instead of being
 * duplicated as a hex constant that would drift from the brand tokens. */
function token(name: string, fallback: string): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}

export interface MetricChartProps {
  /** The series, oldest first, as GET /hosts/{id}/metrics returns it. */
  points: MetricPoint[];
  /** The window the series covers; drives the axis format and the label. */
  range: MetricRange;
  /** What is being plotted, e.g. "disk /var used_percent". Becomes part of
   * the chart's accessible name. */
  label: string;
  height?: number;
}

/**
 * MetricChart draws one numeric metric's series with uPlot — the single
 * charting dependency the architecture spec sanctions.
 *
 * It is presentational on purpose (CLAUDE.md's `src/ui` layer): it takes
 * the points and the window they cover and knows nothing about
 * organizations, hosts, checks or how the series was fetched. That is what
 * lets it be tested against fixture series rather than against a mocked
 * API, which is what the v0.3b-checks spec asks for ("the graph component
 * against fixture series").
 *
 * Only numeric metrics get here at all. A discrete metric (a service
 * state, carried as `label` on the value DTO) has no series, and
 * GET /hosts/{id}/metrics refuses one with a 400 — so the caller
 * (HostChecksCard) offers no graph affordance for it.
 */
export function MetricChart({ points, range, label, height = 180 }: MetricChartProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  // The chart is sized from its container, which has no width until it is
  // in the document — so the first render measures nothing and the effect
  // below re-runs once the real width is known. jsdom reports 0 for every
  // layout box, hence the fallback, which is also what a chart rendered
  // into a display:none panel would get.
  const [width, setWidth] = useState(0);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth);
    measure();
    window.addEventListener('resize', measure);
    return () => window.removeEventListener('resize', measure);
  }, []);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const axisColor = token('--px-border', FALLBACK.border);
    const tickColor = token('--px-muted', FALLBACK.muted);
    const lineColor = token('--px-accent', FALLBACK.accent);
    const plot = new uPlot(
      {
        width: width || 600,
        height,
        // The legend would repeat the accessible name already on the
        // figure, and the cursor readout is the point of hovering.
        legend: { show: false },
        scales: { x: { time: true } },
        axes: [
          { stroke: tickColor, grid: { stroke: axisColor, width: 1 }, ticks: { stroke: axisColor }, values: TICK_FORMAT[range] },
          { stroke: tickColor, grid: { stroke: axisColor, width: 1 }, ticks: { stroke: axisColor }, size: 48 },
        ],
        series: [{}, { label, stroke: lineColor, width: 1.5, points: { show: points.length < 40 } }],
      },
      toAlignedData(points),
      el,
    );
    return () => plot.destroy();
  }, [points, range, label, height, width]);

  return (
    <div className="relative">
      <div
        ref={hostRef}
        role="img"
        aria-label={`${label} over ${RANGE_LABEL[range]}, ${points.length} samples`}
        style={{ height }}
      />
      {points.length === 0 && (
        <span className="pointer-events-none absolute inset-0 flex items-center justify-center text-xs text-muted-foreground">
          No samples in this range.
        </span>
      )}
    </div>
  );
}
