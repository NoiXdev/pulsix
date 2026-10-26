/**
 * The chart — and with it uPlot and its stylesheet, ~25 kB gzipped — is
 * loaded only once an operator opens a graph.
 *
 * A static import would put it in the app's single entry chunk, which
 * every visitor downloads before the login form renders, for a component
 * that appears behind an on-demand click on one card of one page. This is
 * the only dynamic import in the SPA so far; if a second one lands, the
 * pattern is worth naming somewhere shared.
 *
 * Its own module because two files need it — MetricGraphPanel renders the
 * chart, HostCheckValueRow warms the chunk on hover — and a component file
 * that also exports a plain function breaks fast refresh.
 */
export const importMetricChart = () => import('../../ui/MetricChart.tsx');
