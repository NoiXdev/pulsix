import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MetricChart } from './MetricChart.tsx';
import { toAlignedData } from './metricSeries.ts';
import type { MetricPoint } from '../api/types.ts';

/** series builds `count` fixture points, one a minute apart, starting at a
 * fixed instant so nothing here depends on the wall clock. */
function series(count: number): MetricPoint[] {
  const start = Date.UTC(2026, 8, 27, 12, 0, 0);
  return Array.from({ length: count }, (_, i) => ({
    at: new Date(start + i * 60_000).toISOString(),
    value: i * 1.5,
  }));
}

describe('toAlignedData', () => {
  it('maps every point onto uPlot’s two aligned columns, x in seconds', () => {
    const [xs, ys] = toAlignedData(series(3));

    // One x and one y per point — a mapping that drops or pads samples is
    // the defect this pins, and it is the array uPlot itself is handed.
    expect(xs).toHaveLength(3);
    expect(ys).toHaveLength(3);
    // uPlot's time scale is in SECONDS, not the milliseconds Date.parse
    // yields; feeding it milliseconds silently draws the whole series at
    // one x position 55 000 years from now.
    expect(xs[0]).toBe(Date.UTC(2026, 8, 27, 12, 0, 0) / 1000);
    expect(xs[2]).toBe(Date.UTC(2026, 8, 27, 12, 2, 0) / 1000);
    expect(ys).toEqual([0, 1.5, 3]);
  });

  it('answers two empty columns for an empty series', () => {
    expect(toAlignedData([])).toEqual([[], []]);
  });
});

describe('the jsdom canvas stub MetricChart depends on', () => {
  it('answers for 2d only, so feature detection is not lied to', () => {
    const canvas = document.createElement('canvas');
    expect(canvas.getContext('2d')).not.toBeNull();
    // A 2D-shaped proxy returned for these would make any future
    // `getContext('webgl') !== null` check take a branch under test that it
    // would never take in a browser.
    expect(canvas.getContext('webgl')).toBeNull();
    expect(canvas.getContext('bitmaprenderer')).toBeNull();
  });
});

describe('MetricChart', () => {
  it('renders a plot for the number of points it is given', () => {
    const { container } = render(<MetricChart points={series(42)} range="1h" label="cpu usage_percent" />);

    // The accessible name is the only thing a chart drawn on a canvas can
    // say to a screen reader — and to a test.
    expect(screen.getByRole('img', { name: 'cpu usage_percent over the last hour, 42 samples' })).toBeInTheDocument();
    // uPlot really built its DOM: the wrapper, the drawing surface and both
    // axes. Without this the aria-label above would pass with no chart.
    expect(container.querySelector('.uplot')).not.toBeNull();
    expect(container.querySelector('canvas')).not.toBeNull();
    expect(container.querySelectorAll('.u-axis')).toHaveLength(2);
    expect(screen.queryByText('No samples in this range.')).not.toBeInTheDocument();
  });

  it('handles an empty series without throwing, and says so', () => {
    const { container } = render(<MetricChart points={[]} range="7d" label="disk /var used_percent" />);

    expect(screen.getByText('No samples in this range.')).toBeInTheDocument();
    // The frame is still drawn — uPlot is constructed with the empty series
    // rather than skipped, which is the case that used to throw.
    expect(container.querySelector('.uplot')).not.toBeNull();
    expect(screen.getByRole('img', { name: 'disk /var used_percent over the last 7 days, 0 samples' })).toBeInTheDocument();
  });

  it('names the window it is drawing in its accessible label', () => {
    const { rerender } = render(<MetricChart points={series(2)} range="24h" label="memory used_percent" />);
    expect(screen.getByRole('img', { name: /over the last 24 hours, 2 samples/ })).toBeInTheDocument();

    rerender(<MetricChart points={series(5)} range="7d" label="memory used_percent" />);
    expect(screen.getByRole('img', { name: /over the last 7 days, 5 samples/ })).toBeInTheDocument();
  });
});
