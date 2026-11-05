import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Card } from '../../../ui/table.tsx';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { ListRow, ListRowName } from '../../../ui/list-row.tsx';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { CreateGroupDialog } from '../CreateGroupDialog.tsx';
import { useT } from '../../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 1fr) 120px';

/** ChildrenSection lists the group's child groups, each a row to its page;
 * Add child group sits in the section header for a writer. */
export function ChildrenSection({
  org, group, groups, canWrite,
}: {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  canWrite: boolean;
}) {
  const t = useT();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const children = groups.filter((g) => g.parent_id === group.id);
  return (
    <Section
      id="children"
      title={t.inventory.detail.section.children}
      actions={
        canWrite ? (
          <Button variant="primary" onClick={() => setOpen(true)}>
            {t.inventory.children.add}
          </Button>
        ) : undefined
      }
    >
      <span className="text-xs text-muted-foreground">{t.inventory.children.intro}</span>
      <Card>
        {children.map((c) => (
          <ListRow key={c.id} to="/o/$org/groups/$id" params={{ org, id: c.id }} search={{ tab: undefined }} columns={COLUMNS} label={c.name}>
            <ListRowName className="truncate font-mono text-[13px]">{c.name}</ListRowName>
            <span className="text-xs text-muted-foreground">{t.inventory.children.members(c.members)}</span>
          </ListRow>
        ))}
        {children.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.inventory.children.empty}</p>}
      </Card>
      {canWrite && (
        <CreateGroupDialog
          org={org}
          open={open}
          onClose={() => setOpen(false)}
          groups={groups}
          fixedParent={group.id}
          onCreated={(id) => {
            setOpen(false);
            void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: undefined }, hash: 'schema' });
          }}
        />
      )}
    </Section>
  );
}
