import { Link } from '@tanstack/react-router';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useHostInventoryGroups } from '../../api/hooks/useInventory.ts';
import { useT } from '../../i18n/useT.ts';

/** HostInventoryCard lists the inventory groups a managed host is a direct
 * member of, with whether its values are complete. It has no title of its
 * own: the host page's Inventory section names it. */
export function HostInventoryCard({ org, hostId, managed }: { org: string; hostId: string; managed: boolean }) {
  const t = useT();
  const groups = useHostInventoryGroups(org, managed ? hostId : '');
  return (
    <Card className="p-4">
      {!managed && <p className="text-xs text-muted-foreground">{t.hosts.inventory.needsManaged}</p>}
      {managed && groups.data && (groups.data.groups ?? []).length === 0 && <p className="text-xs text-muted-foreground">{t.hosts.inventory.none}</p>}
      {managed && (
        <ul className="flex flex-col gap-2">
          {(groups.data?.groups ?? []).map((m) => (
            <li key={m.group_id} className="flex items-center justify-between gap-2">
              <Link to="/o/$org/groups/$id" params={{ org, id: m.group_id }} search={{ tab: 'members' }} className="font-mono text-xs hover:text-primary">
                {m.group_name}
              </Link>
              {m.complete ? <Pill tone="ok">{t.hosts.inventory.complete}</Pill> : <Pill tone="warn">{t.hosts.inventory.toFill((m.problems ?? []).length)}</Pill>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
