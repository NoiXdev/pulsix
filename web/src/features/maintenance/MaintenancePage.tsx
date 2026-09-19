import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Wrench } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useDeleteMaintenanceWindow, useMaintenanceWindows } from '../../api/hooks/useMaintenance.ts';
import { may } from '../../lib/roles.ts';
import type { MaintenanceWindow } from '../../api/types.ts';
import { WindowDialog } from './WindowDialog.tsx';
import { targetLabel, whenLabel } from './time.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 0.8fr) minmax(0, 1.4fr) 110px 150px';

/** MaintenancePage lists the organization's maintenance windows (spec
 * 2026-09-18 §6): what each covers, when, and whether it is active now.
 * Schedules can skip runs, or leave out hosts, during a window. */
export function MaintenancePage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
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
    if (!confirm(`Delete maintenance window ${w.name}?`)) return;
    try {
      await remove.mutateAsync(w.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Maintenance</h1>
          <span className="text-[13px] text-muted-foreground">
            {list.filter((w) => w.active_now).length} active now · schedules can skip runs or leave out hosts during a window
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Wrench size={15} /> New window
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Window</span>
          <span>Applies to</span>
          <span>When</span>
          <span>Status</span>
          <span />
        </GridHeader>
        {list.map((w) => (
          <GridRow key={w.id} columns={COLUMNS}>
            <span className="flex min-w-0 flex-col" data-row>
              <span className="truncate text-[13px] font-medium">{w.name}</span>
              {w.note && <span className="truncate text-[11px] text-muted-foreground">{w.note}</span>}
            </span>
            <span className="truncate text-xs">{targetLabel(w)}</span>
            <span className="truncate text-xs text-muted-foreground">{whenLabel(w)}</span>
            <span>{w.active_now ? <Pill tone="warn">active now</Pill> : <span className="text-xs text-muted-foreground">—</span>}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={`Edit ${w.name}`} onClick={() => setEditing(w)}>
                    Edit
                  </Button>
                  <Button type="button" variant="destructive" aria-label={`Delete ${w.name}`} onClick={() => void onDelete(w)}>
                    Delete
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No maintenance windows.</p>}
      </Card>
      {editing && <WindowDialog org={org} window={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  );
}
