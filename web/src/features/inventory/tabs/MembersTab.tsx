import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Card, GridHeader, GridRow } from '../../../ui/table.tsx';
import { Button } from '../../../ui/button.tsx';
import { Pill } from '../../../ui/badge.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useGroupMembers, useUnassignHost } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { AssignHostDialog } from './AssignHostDialog.tsx';

const COLUMNS = 'minmax(0, 1fr) 200px 150px';

export function MembersTab({ org, group }: { org: string; group: InventoryGroup }) {
  const toast = useToast();
  const members = useGroupMembers(org, group.id);
  const unassign = useUnassignHost(org, group.id);
  const [dialog, setDialog] = useState<{ member?: InventoryMember } | null>(null);

  const guard = useQueryGuard(members);
  if (guard) return guard;
  if (!members.data) return <QueryLoading />;
  const list = members.data.members ?? [];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {list.length} direct members · {list.filter((m) => !m.complete).length} need values
        </span>
        <Button variant="primary" onClick={() => setDialog({})}>
          Assign host
        </Button>
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Host</span>
          <span>Values</span>
          <span />
        </GridHeader>
        {list.map((m) => (
          <GridRow key={m.host_id} columns={COLUMNS}>
            <Link to="/o/$org/hosts/$id" params={{ org, id: m.host_id }} className="truncate font-mono text-[13px] hover:text-primary">
              {m.host_name}
            </Link>
            <span>{m.complete ? <Pill tone="ok">complete</Pill> : <Pill tone="warn">{(m.problems ?? []).length} to fill in</Pill>}</span>
            <span className="flex justify-end gap-1">
              <Button variant="ghost" onClick={() => setDialog({ member: m })} aria-label={`Edit values of ${m.host_name}`}>
                Edit
              </Button>
              <Button
                variant="ghost"
                aria-label={`Remove ${m.host_name}`}
                onClick={async () => {
                  if (!confirm(`Remove ${m.host_name} from ${group.name}? Its values for this group are deleted.`)) return;
                  try {
                    await unassign.mutateAsync(m.host_id);
                    toast.show(`${m.host_name} removed from ${group.name}`);
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'Removing the host failed', 'destructive');
                  }
                }}
              >
                Remove
              </Button>
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No hosts in this group yet.</p>}
      </Card>
      {dialog && (
        <AssignHostDialog org={org} group={group} member={dialog.member} existing={list.map((m) => m.host_id)} onClose={() => setDialog(null)} />
      )}
    </div>
  );
}
