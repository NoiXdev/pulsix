import type { MetricPoint } from '../api/types.ts';

/**
 * toAlignedData turns the API's points into uPlot's column layout: one
 * array of x values, one of y values, index-aligned.
 *
 * The x values are **seconds**, not the milliseconds `Date.parse` returns.
 * uPlot's time scale reads its x values as a Unix timestamp in seconds;
 * handing it milliseconds does not fail, it silently places the whole
 * series tens of thousands of years into the future and draws an empty
 * plot — which is why this conversion is pinned by its own test rather
 * than left inline in MetricChart's effect.
 *
 * It sits beside MetricChart.tsx rather than inside it for the same reason
 * useQueryGuard sits beside QueryState.tsx: a file that exports a
 * component may not also export a plain function without breaking fast
 * refresh (`react-refresh/only-export-components`).
 */
export function toAlignedData(points: MetricPoint[]): [number[], number[]] {
  const xs: number[] = [];
  const ys: number[] = [];
  for (const p of points) {
    xs.push(Date.parse(p.at) / 1000);
    ys.push(p.value);
  }
  return [xs, ys];
}
