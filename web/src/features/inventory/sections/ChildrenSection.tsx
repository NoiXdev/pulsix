import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { Card, GridRow } from '../../../ui/table.tsx';
import { Button } from '../../../ui/button.tsx';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { CreateGroupDialog } from '../CreateGroupDialog.tsx';
import { useT } from '../../../i18n/useT.ts';

export function ChildrenSection({ org, group, groups }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[] }) {
  const t = useT();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const children = groups.filter((g) => g.parent_id === group.id);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">{t.inventory.children.intro}</span>
        <Button variant="primary" onClick={() => setOpen(true)}>
          {t.inventory.children.add}
        </Button>
      </div>
      <Card>
        {children.map((c) => (
          <GridRow key={c.id} columns="minmax(0, 1fr) 120px">
            <Link to="/o/$org/groups/$id" params={{ org, id: c.id }} search={{ tab: undefined }} className="font-mono text-[13px] hover:text-primary">
              {c.name}
            </Link>
            <span className="text-xs text-muted-foreground">{t.inventory.children.members(c.members)}</span>
          </GridRow>
        ))}
        {children.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.inventory.children.empty}</p>}
      </Card>
      <CreateGroupDialog
        org={org}
        open={open}
        onClose={() => setOpen(false)}
        groups={groups}
        fixedParent={group.id}
        onCreated={(id) => {
          setOpen(false);
          void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: 'schema' } });
        }}
      />
    </div>
  );
}
