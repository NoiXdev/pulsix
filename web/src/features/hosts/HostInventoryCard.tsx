import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useHostInventoryGroups } from '../../api/hooks/useInventory.ts';

/** HostInventoryCard lists the inventory groups a managed host is a direct
 * member of, with whether its values are complete. */
export function HostInventoryCard({ org, hostId, managed }: { org: string; hostId: string; managed: boolean }) {
  const groups = useHostInventoryGroups(org, managed ? hostId : '');
  return (
    <Card className="p-4">
      <h2 className="mb-3 text-[13px] font-semibold">Inventory groups</h2>
      {!managed && <p className="text-xs text-muted-foreground">Inventory groups need the Managed capability.</p>}
      {managed && groups.data && (groups.data.groups ?? []).length === 0 && <p className="text-xs text-muted-foreground">Not in any inventory group yet.</p>}
      {managed && (
        <ul className="flex flex-col gap-2">
          {(groups.data?.groups ?? []).map((m) => (
            <li key={m.group_id} className="flex items-center justify-between gap-2">
              <Link to="/o/$org/groups/$id" params={{ org, id: m.group_id }} search={{ tab: 'members' }} className="font-mono text-xs hover:text-primary">
                {m.group_name}
              </Link>
              {m.complete ? <Pill tone="ok">complete</Pill> : <Pill tone="warn">{(m.problems ?? []).length} to fill in</Pill>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
