import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Wrench } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useDeleteMaintenanceWindow, useMaintenanceWindows } from '../../api/hooks/useMaintenance.ts';
import { may } from '../../lib/roles.ts';
import type { MaintenanceWindow } from '../../api/types.ts';
import { WindowDialog } from './WindowDialog.tsx';
import { targetLabel, whenLabel } from './time.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS = 'minmax(0, 0.8fr) 90px minmax(0, 0.8fr) minmax(0, 1.3fr) 110px 150px';

/** MaintenancePage lists the organization's windows (spec 2026-09-18 §6
 * and v0.4a decision 3): why each exists, what it covers, when, and
 * whether it is active now.
 *
 * "Why" is a column rather than something only the edit dialog knows
 * (v0.4a final review, Important): the two reasons do entirely different
 * things — a maintenance window lets schedules skip runs on the hosts it
 * covers, a silence window mutes their alerts and leaves runs alone — and
 * a list that rendered them identically left an operator with no way to
 * tell one from the other short of opening each in turn. */
export function MaintenancePage() {
  const t = useT();
  const fmt = useFormat();
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'maintenance.write');
  const windows = useMaintenanceWindows(org);
  const remove = useDeleteMaintenanceWindow(org);
  const [editing, setEditing] = useState<MaintenanceWindow | 'new' | null>(null);

  const guard = useQueryGuard(windows);
  if (guard) return guard;
  if (!windows.data) return <QueryLoading />;
  const list = windows.data.windows ?? [];

  async function onDelete(w: MaintenanceWindow) {
    if (!(await ask({ message: t.maintenance.deleteConfirm[w.reason === 'silence' ? 'silence' : 'maintenance'](w.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(w.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.maintenance.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.maintenance.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.maintenance.active(list.filter((w) => w.active_now).length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Wrench size={15} /> {t.maintenance.newWindow}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.maintenance.columns.window}</span>
          <span>{t.maintenance.columns.why}</span>
          <span>{t.maintenance.columns.appliesTo}</span>
          <span>{t.maintenance.columns.when}</span>
          <span>{t.maintenance.columns.status}</span>
          <span />
        </GridHeader>
        {list.map((w) => (
          <GridRow key={w.id} columns={COLUMNS}>
            <span className="flex min-w-0 flex-col" data-row>
              <span className="truncate text-[13px] font-medium">{w.name}</span>
              {w.note && <span className="truncate text-[11px] text-muted-foreground">{w.note}</span>}
            </span>
            <span>
              <Pill tone={w.reason === 'silence' ? 'unknown' : 'maintenance'}>{t.maintenance.reason[w.reason === 'silence' ? 'silence' : 'maintenance']}</Pill>
            </span>
            <span className="truncate text-xs">{targetLabel(w, t)}</span>
            <span className="truncate text-xs text-muted-foreground">{whenLabel(w, t, fmt)}</span>
            <span>{w.active_now ? <Pill tone="maintenance">{t.maintenance.activeNow}</Pill> : <span className="text-xs text-muted-foreground">—</span>}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={t.maintenance.editAria(w.name)} onClick={() => setEditing(w)}>
                    {t.maintenance.edit}
                  </Button>
                  <Button type="button" variant="destructive" aria-label={t.maintenance.deleteAria(w.name)} onClick={() => void onDelete(w)}>
                    {t.common.delete}
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.maintenance.empty}</p>}
      </Card>
      {editing && <WindowDialog org={org} window={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  );
}
