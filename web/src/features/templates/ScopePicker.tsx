import { useState } from 'react';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { orderTree } from '../inventory/tree.ts';

export interface Scope {
  /** "" = whole organization. */
  group_id: string;
  host_ids: string[];
}

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

/**
 * ScopePicker is the job template's target picker, reusing the inventory
 * pieces (the group tree for the dropdown, the hosts list with its search
 * for the host checkboxes): a group, an explicit host selection, or both;
 * nothing selected runs against every managed host of the organization.
 */
export function ScopePicker({ org, value, onChange }: { org: string; value: Scope; onChange: (next: Scope) => void }) {
  const [q, setQ] = useState('');
  const groups = useGroups(org);
  const hosts = useHosts(org, { managed: true, q });
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = hosts;
  const rows = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];

  const toggleHost = (id: string) => {
    const next = value.host_ids.includes(id) ? value.host_ids.filter((h) => h !== id) : [...value.host_ids, id];
    onChange({ ...value, host_ids: next });
  };

  return (
    <fieldset className="flex flex-col gap-2 rounded-md border border-border p-3">
      <legend className="px-1 text-[11px] font-semibold">Scope</legend>
      <div className="flex flex-col gap-1">
        <label htmlFor="scope-group" className="text-[11px] font-medium text-muted-foreground">
          Group
        </label>
        <select
          id="scope-group"
          className={SELECT}
          value={value.group_id}
          onChange={(e) => onChange({ ...value, group_id: e.target.value })}
        >
          <option value="">Whole organization</option>
          {orderTree(groups.data?.groups ?? []).map(({ group, depth }) => (
            <option key={group.id} value={group.id}>
              {'\u00A0'.repeat(depth * 2)}
              {group.name}
            </option>
          ))}
        </select>
      </div>
      <div className="flex flex-col gap-1">
        <label htmlFor="scope-hosts" className="text-[11px] font-medium text-muted-foreground">
          Hosts ({value.host_ids.length} selected)
        </label>
        <input
          id="scope-hosts"
          type="search"
          placeholder="Search managed hosts…"
          className={SELECT}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <div className="max-h-40 overflow-y-auto rounded-md border border-border">
          {rows.map((h) => (
            <label key={h.id} className="flex items-center gap-2 px-2 py-1 text-xs">
              <input
                type="checkbox"
                checked={value.host_ids.includes(h.id)}
                onChange={() => toggleHost(h.id)}
                aria-label={`Include ${h.name}`}
              />
              <span className="font-mono">{h.name}</span>
            </label>
          ))}
          {!hosts.isPending && rows.length === 0 && (
            <p className="px-3 py-2 text-[11px] text-muted-foreground">No managed host matches.</p>
          )}
          {hasNextPage && <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />}
        </div>
      </div>
      <span className="text-[11px] text-muted-foreground">Nothing selected runs against every managed host.</span>
    </fieldset>
  );
}
