import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { DataList } from '../../../ui/data-list/index.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { useListView } from '../../lists/useListView.ts';
import { CreateGroupDialog } from '../CreateGroupDialog.tsx';
import { childColumns } from './childColumns.tsx';
import { useT } from '../../../i18n/useT.ts';

/** ChildrenSection lists the group's child groups (list id `group-children`,
 * sorted and paged in the browser, never in the URL), each a row to its page;
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
  const columns = childColumns(t);
  const view = useListView('group-children', columns, { mode: 'client', urlState: false, rowKey: (g) => g.id });
  const { rows, total } = view.view(view.ready ? children : undefined);
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
      <DataList
        label={t.inventory.children.label}
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowLink={(c) => ({ to: '/o/$org/groups/$id', params: { org, id: c.id }, search: { tab: undefined }, label: c.name })}
        loading={!view.ready}
        empty={t.inventory.children.empty}
        {...view.listProps(total, t.inventory.children.count)}
      />
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
