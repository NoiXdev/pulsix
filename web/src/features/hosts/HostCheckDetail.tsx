import { HostCheckValueRow } from './HostCheckValueRow.tsx';
import { HostCheckVantages } from './HostCheckVantages.tsx';
import { HostCheckHttp } from './HostCheckHttp.tsx';
import type { CheckPluginDescriptor, EffectiveRule, HostCheck, MonitoringGroup } from '../../api/types.ts';
import type { MetricSelection } from '../../api/hooks/useChecks.ts';
import type { MetricKinds, TupleKey } from './hostChecks.ts';
import { useT } from '../../i18n/useT.ts';

export interface HostCheckDetailProps {
  org: string;
  hostId: string;
  canWrite: boolean;
  frozen: boolean;
  check: HostCheck;
  plugins: readonly CheckPluginDescriptor[] | undefined;
  kinds: MetricKinds;
  open: MetricSelection | null;
  onOpen: (s: MetricSelection | null) => void;
  effective: EffectiveRule[];
  groups: MonitoringGroup[];
  openRules: TupleKey | null;
  onOpenRules: (k: TupleKey | null) => void;
}

/**
 * HostCheckDetail is what an expanded row of the Checks table shows for one
 * check: every metric with its verdict and item decisions (Ignore, Rules,
 * Graph), an http check's timing and assertions, and the vantages of a
 * check probed from several places.
 */
export function HostCheckDetail({ org, hostId, canWrite, frozen, check: c, plugins, kinds, open, onOpen, effective, groups, openRules, onOpenRules }: HostCheckDetailProps) {
  const t = useT();
  return (
    <>
      <ul className="flex flex-col gap-1.5">
        {(c.values ?? []).map((v) => (
          <HostCheckValueRow
            key={`${v.item}:${v.metric}`}
            org={org}
            hostId={hostId}
            canWrite={canWrite}
            frozen={frozen}
            check={c}
            value={v}
            kinds={kinds}
            open={open}
            onOpen={onOpen}
            effective={effective}
            groups={groups}
            openRules={openRules}
            onOpenRules={onOpenRules}
          />
        ))}
        {(c.values ?? []).length === 0 && <li className="text-[11px] text-muted-foreground">{t.hosts.checks.noReading}</li>}
      </ul>
      {/* Backlog 3.13b: an http check's timing breakdown and assertions,
          below its values. */}
      {c.plugin === 'http' && <HostCheckHttp check={c} />}
      {/* Backlog 3.13a: where a check probed from several places was
          probed from, below its combined values. */}
      {c.vantages && <HostCheckVantages check={c} plugins={plugins} />}
    </>
  );
}
