import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import type uPlot from 'uplot';
import { MetricChart } from './MetricChart.tsx';
import { toAlignedData } from './metricSeries.ts';
import type { MetricPoint } from '../api/types.ts';
import { resizeTo } from '../test/resizeObserver.ts';
import { I18nProvider } from '../i18n/I18nProvider.tsx';

/**
 * The real uPlot, wrapped so the test can see how many instances a render
 * sequence built and what data each ended up holding. Nothing is faked —
 * every assertion elsewhere in this file still runs against the genuine
 * library and the DOM it produces — but "did this rebuild the plot or feed
 * the existing one?" is otherwise unobservable: both paths leave a canvas
 * on screen and an identical accessible name.
 */
const spy = vi.hoisted(() => ({ built: [] as uPlot[] }));
vi.mock('uplot', async (importOriginal) => {
  const actual = await importOriginal<{ default: typeof uPlot }>();
  const Wrapped = function (this: unknown, ...args: ConstructorParameters<typeof uPlot>) {
    const instance = new actual.default(...args);
    spy.built.push(instance);
    return instance;
  };
  return { ...actual, default: Wrapped };
});

beforeEach(() => {
  spy.built = [];
});

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

  it('feeds new points to the existing plot instead of rebuilding it', () => {
    const { container, rerender } = render(<MetricChart points={series(3)} range="1h" label="cpu usage_percent" />);
    const canvas = container.querySelector('canvas');
    expect(spy.built).toHaveLength(1);

    // A poll returning one more sample is a new array identity every 30 s.
    // Tearing the plot down for that resets the cursor crosshair under
    // whoever is hovering it.
    rerender(<MetricChart points={series(4)} range="1h" label="cpu usage_percent" />);

    expect(spy.built).toHaveLength(1);
    expect(container.querySelector('canvas')).toBe(canvas);
    // …and the existing instance really received the new series, which the
    // unchanged canvas alone would not prove.
    expect(spy.built[0].data[0]).toHaveLength(4);
    expect(spy.built[0].data[1]).toEqual([0, 1.5, 3, 4.5]);
    expect(screen.getByRole('img', { name: /4 samples/ })).toBeInTheDocument();
  });

  it('stops drawing point markers as a long-open chart fills up', () => {
    const { rerender } = render(<MetricChart points={series(39)} range="1h" label="cpu usage_percent" />);
    const plot = spy.built[0];
    // uPlot evaluates points.show at draw time, so this asks it the same
    // question the renderer does, against the instance's live data.
    const markers = () => {
      const show = plot.series[1].points?.show;
      return typeof show === 'function' ? show(plot, 1, 0, Math.max(plot.data[0].length - 1, 0)) : show;
    };

    expect(markers()).toBe(true);

    // `points` is no longer a construction dependency, so a boolean fixed
    // at construction would freeze at "39 < 40" and keep drawing dots for
    // as long as the graph stays open.
    rerender(<MetricChart points={series(41)} range="1h" label="cpu usage_percent" />);

    expect(spy.built).toHaveLength(1);
    expect(plot.data[0]).toHaveLength(41);
    expect(markers()).toBe(false);
  });

  it('rebuilds the plot for a change setData cannot carry', () => {
    const { container, rerender } = render(<MetricChart points={series(3)} range="1h" label="cpu usage_percent" />);
    const canvas = container.querySelector('canvas');

    // The x-axis tick format is baked into the options at construction, so
    // a range switch is one of the few changes that genuinely needs a new
    // instance.
    rerender(<MetricChart points={series(3)} range="7d" label="cpu usage_percent" />);

    expect(spy.built).toHaveLength(2);
    expect(container.querySelector('canvas')).not.toBe(canvas);
  });

  it('re-measures when its container resizes and no window event fires', () => {
    const { container } = render(<MetricChart points={series(3)} range="1h" label="cpu usage_percent" />);
    const host = container.querySelector('[role="img"]') as HTMLElement;
    expect(spy.built).toHaveLength(1);

    // A sidebar collapsing or a panel opening beside the chart changes its
    // container's width with no window resize behind it — the only event a
    // resize listener would ever hear (backlog 3.26).
    act(() => resizeTo(host, 420));

    // The width is baked into the uPlot options at construction, so
    // noticing the new one means a new instance at that width. A chart
    // that missed it would sit at its old size until the next window
    // resize, which may never come.
    expect(spy.built).toHaveLength(2);
    expect(spy.built[1].width).toBe(420);
  });

  it('does not rebuild when a resize reports the width it already has', () => {
    const { container } = render(<MetricChart points={series(3)} range="1h" label="cpu usage_percent" />);
    const host = container.querySelector('[role="img"]') as HTMLElement;

    act(() => resizeTo(host, 420));
    expect(spy.built).toHaveLength(2);

    // A ResizeObserver fires for height changes too, and uPlot's own
    // canvas sizing can provoke one. Rebuilding on every notification
    // would tear the plot down under a hovering cursor for nothing.
    act(() => resizeTo(host, 420));

    expect(spy.built).toHaveLength(2);
  });

  it('names the window it is drawing in its accessible label', () => {
    const { rerender } = render(<MetricChart points={series(2)} range="24h" label="memory used_percent" />);
    expect(screen.getByRole('img', { name: /over the last 24 hours, 2 samples/ })).toBeInTheDocument();

    rerender(<MetricChart points={series(5)} range="7d" label="memory used_percent" />);
    expect(screen.getByRole('img', { name: /over the last 7 days, 5 samples/ })).toBeInTheDocument();
  });
});

describe('MetricChart in German', () => {
  it.each([
    ['1h', 'cpu usage_percent, in der letzten Stunde, 3 Messwerte'],
    ['24h', 'cpu usage_percent, in den letzten 24 Stunden, 3 Messwerte'],
    ['7d', 'cpu usage_percent, in den letzten 7 Tagen, 3 Messwerte'],
  ] as const)('names the %s chart with a grammatical German phrase', (range, name) => {
    render(
      <I18nProvider locale="de">
        <MetricChart points={series(3)} range={range} label="cpu usage_percent" />
      </I18nProvider>,
    );
    expect(screen.getByRole('img', { name })).toBeInTheDocument();
  });

  it('says the empty state in German', () => {
    render(
      <I18nProvider locale="de">
        <MetricChart points={[]} range="1h" label="cpu" />
      </I18nProvider>,
    );
    expect(screen.getByText('Keine Messwerte in diesem Zeitraum.')).toBeInTheDocument();
  });
});
