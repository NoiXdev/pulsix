/** The drawing's own coordinate space; the SVG stretches it to its box. */
const WIDTH = 100;
const HEIGHT = 30;

/**
 * Sparkline is a small line of a series' shape, without axes, for a tile
 * that shows a current value and wants its recent trend beside it. An SVG
 * rather than MetricChart: a page with four of them should not load the
 * charting library for what is a single polyline.
 *
 * `domain` fixes the vertical scale (0–100 for a percentage, so a flat 90 %
 * reads high); without it the line spans the series' own range. Fewer than
 * two values draw nothing but keep the space, so tiles stay aligned.
 */
export function Sparkline({
  values,
  domain,
  label,
  color = 'var(--px-accent)',
  className,
}: {
  values: readonly number[];
  domain?: [number, number];
  label: string;
  color?: string;
  className?: string;
}) {
  const [lo, hi] = domain ?? [Math.min(...values), Math.max(...values)];
  const span = hi - lo;
  const y = (v: number) => (span === 0 ? HEIGHT / 2 : HEIGHT - ((Math.min(Math.max(v, lo), hi) - lo) / span) * HEIGHT);
  const x = (i: number) => (i / (values.length - 1)) * WIDTH;
  const points = values.map((v, i) => `${round(x(i))},${round(y(v))}`).join(' ');

  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      preserveAspectRatio="none"
      className={className ?? 'h-8 w-full'}
    >
      {values.length >= 2 && (
        <polyline points={points} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
      )}
    </svg>
  );
}

function round(n: number): number {
  return Math.round(n * 100) / 100;
}
