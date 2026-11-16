import { VantageTable } from '../../ui/VantageTable.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import type { CheckPluginDescriptor, HostCheck } from '../../api/types.ts';
import { vantageRows } from './vantages.ts';

/**
 * HostCheckVantages is a synthetic check's probing points on the host page
 * (backlog 3.13a): the fail policy that combines them, then one row per
 * probing point with its state, response time and age, in the order the
 * combined result picks one. The values above it on the card are the
 * combined result itself.
 */
export function HostCheckVantages({ check, plugins }: { check: HostCheck; plugins: readonly CheckPluginDescriptor[] | undefined }) {
  const t = useT();
  const format = useFormat();
  const rows = vantageRows(check, plugins, t, format, new Date());
  return (
    <div className="mt-2 flex flex-col gap-1 rounded-md border border-border p-2">
      {check.fail_policy && <p className="text-[11px] text-muted-foreground">{t.hosts.vantages.policy[check.fail_policy]}</p>}
      {rows.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">{t.hosts.vantages.none}</p>
      ) : (
        <VantageTable label={t.hosts.vantages.title(check.key)} columns={t.hosts.vantages.columns} rows={rows} />
      )}
    </div>
  );
}
