import { AssertionTable } from '../../ui/AssertionTable.tsx';
import { StackedBar } from '../../ui/StackedBar.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import type { HostCheck } from '../../api/types.ts';
import { assertionRows, timingParts } from './httpCheck.ts';

/**
 * HostCheckHttp is an http check's own section on the host page (backlog
 * 3.13b), below its values: the latest attempt's timing breakdown as one
 * stacked bar (the phases it reported, with their times) and its
 * assertions as a table. Either is left out when there is nothing to show.
 */
export function HostCheckHttp({ check }: { check: HostCheck }) {
  const t = useT();
  const format = useFormat();
  const parts = timingParts(check, t);
  const rows = assertionRows(check, t, format, new Date());
  if (parts.length === 0 && rows.length === 0) return null;
  return (
    <div className="mt-2 flex flex-col gap-2 rounded-md border border-border p-2">
      {parts.length > 0 && <StackedBar label={t.hosts.http.timingTitle(check.key)} parts={parts} />}
      {rows.length > 0 && <AssertionTable label={t.hosts.http.assertionsTitle(check.key)} columns={t.hosts.http.columns} rows={rows} />}
    </div>
  );
}
