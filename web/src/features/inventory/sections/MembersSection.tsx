import { useState, type ReactNode } from 'react';
import { Card, GridHeader } from '../../../ui/table.tsx';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useGroupMembers } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { AssignHostDialog } from './AssignHostDialog.tsx';
import { MemberRow } from './MemberRow.tsx';
import { useT } from '../../../i18n/useT.ts';

/** Host name, completeness, and the 40px the row menu sits over: every
 * member row has a menu (Open host is there for every role). */
const COLUMNS = 'minmax(0, 1fr) 200px 40px';

/** MembersSection lists the group's direct members, each a row to its host
 * page; Assign host sits in the section header for a writer. */
export function MembersSection({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const members = useGroupMembers(org, group.id);
  const [dialog, setDialog] = useState<{ member?: InventoryMember } | null>(null);
  const list = members.data?.members ?? [];

  return (
    <Section
      id="members"
      title={t.inventory.detail.section.members}
      actions={
        canWrite ? (
          // Disabled until the members are known: the picker leaves them out.
          <Button variant="primary" disabled={!members.data} onClick={() => setDialog({})}>
            {t.inventory.members.assign}
          </Button>
        ) : undefined
      }
    >
      <MembersList members={members} columns={COLUMNS}>
        {list.map((m) => (
          <MemberRow key={m.host_id} org={org} group={group} member={m} columns={COLUMNS} canWrite={canWrite} onEdit={(member) => setDialog({ member })} />
        ))}
      </MembersList>
      {dialog && (
        <AssignHostDialog org={org} group={group} member={dialog.member} existing={list.map((m) => m.host_id)} onClose={() => setDialog(null)} />
      )}
    </Section>
  );
}

/** The card around the rows, with the query's loading and error states in
 * its place while there are no rows to show. */
function MembersList({ members, columns, children }: { members: ReturnType<typeof useGroupMembers>; columns: string; children: ReactNode }) {
  const t = useT();
  const guard = useQueryGuard(members);
  if (guard) return guard;
  if (!members.data) return <QueryLoading />;
  const empty = (members.data.members ?? []).length === 0;
  return (
    <Card>
      <GridHeader columns={columns}>
        <span>{t.inventory.members.columns.host}</span>
        <span>{t.inventory.members.columns.values}</span>
        <span />
      </GridHeader>
      {children}
      {empty && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.inventory.members.empty}</p>}
    </Card>
  );
}
