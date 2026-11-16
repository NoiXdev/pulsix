import type { Tone } from '../../ui/badge.tsx';
import type { SelectOption } from '../../ui/Select.tsx';
import type { CheckPluginDescriptor, HostCheck } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { formatNumber } from './hostChecks.ts';

/**
 * The host page's reading of a check's vantage points (backlog 3.13a):
 * GET /hosts/{id}/checks lists, for a synthetic check probed from several
 * places, every expected probing point in the order its combined result
 * picks one — the Pulsix server, then the agents by host name — with the
 * state the server read off its latest round. These turn that list into
 * the rows of the vantage table and the choices of the graph's selector;
 * the state itself is never recomputed here.
 */

type Vantage = NonNullable<HostCheck['vantages']>[number];

/** VantageRow is one probing point as the table shows it. */
export interface VantageRow {
  key: string;
  name: string;
  status: { label: string; tone: Tone };
  /** The latest round's response time, or "—". */
  response: string;
  /** How long ago the latest round was, or "—" before the first. */
  age: string;
}

/** responseMetricOf is the plugin's response time metric — the one metric
 * of a synthetic plugin measured in milliseconds — or undefined for a
 * plugin without one, or while the descriptors are not known. */
export function responseMetricOf(plugin: string, plugins: readonly CheckPluginDescriptor[] | undefined): string | undefined {
  return plugins?.find((p) => p.name === plugin)?.metrics.find((m) => m.name.endsWith('_ms'))?.name;
}

/** vantageName is how a probing point is named: the server by its own
 * name, an agent by its host's, or by its id when that host has none. */
function vantageName(v: Vantage, t: Messages): string {
  if (v.kind === 'manager') return t.hosts.vantages.server;
  return v.name || v.vantage;
}

const TONES: Record<Vantage['status'], Tone> = { up: 'ok', down: 'crit', none: 'unknown' };

/** vantageRows are check's probing points as table rows; none for a check
 * without vantage groups. */
export function vantageRows(
  check: HostCheck,
  plugins: readonly CheckPluginDescriptor[] | undefined,
  t: Messages,
  format: Format,
  now: Date,
): VantageRow[] {
  const metric = responseMetricOf(check.plugin, plugins);
  return (check.vantages ?? []).map((v) => {
    const ms = metric ? (v.observations ?? []).find((o) => o.item === '' && o.metric === metric)?.value : undefined;
    return {
      key: v.vantage,
      name: vantageName(v, t),
      status: { label: t.hosts.vantages.states[v.status], tone: TONES[v.status] },
      response: ms === undefined ? '—' : t.hosts.vantages.ms(formatNumber(ms)),
      age: v.at ? format.relative(v.at, now) : '—',
    };
  });
}

/** vantageOptions are the graph's series choices for check: the combined
 * one (no vantage, the check's own series) first, then each probing
 * point; undefined for a check without vantage groups. */
export function vantageOptions(check: HostCheck, t: Messages): SelectOption[] | undefined {
  if (!check.vantages) return undefined;
  return [{ value: '', label: t.hosts.vantages.combined }, ...check.vantages.map((v) => ({ value: v.vantage, label: vantageName(v, t) }))];
}
