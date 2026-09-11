import { useEffect, useState } from 'react';
import { Label } from '../../../ui/label.tsx';
import { LoadMoreButton } from '../../../ui/LoadMoreButton.tsx';
import { useHosts } from '../../../api/hooks/useHosts.ts';

const FIELD = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

/** ManagedHostPicker chooses a managed host that is not yet a member. The
 * hosts list is paginated, so the picker searches through the API (`q`)
 * rather than filtering one page: every managed host is reachable, a hint
 * says when more hosts match than are listed, and "every managed host is
 * already a member" is shown only once no page holds another one. */
export function ManagedHostPicker({
  org, existing, value, onChange,
}: {
  org: string;
  existing: readonly string[];
  value: string;
  onChange: (hostId: string) => void;
}) {
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { managed: true, q });
  const assignable = (hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? []).filter((h) => !existing.includes(h.id));
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = hosts;

  // A loaded page may hold members only; fetch on until a candidate shows
  // up or the pages run out, so an empty list always means "none left".
  useEffect(() => {
    if (hosts.data && assignable.length === 0 && hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hosts.data, assignable.length, hasNextPage, isFetchingNextPage, fetchNextPage]);

  const exhausted = !!hosts.data && !hasNextPage && assignable.length === 0;

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="assign-host">Host</Label>
      <input
        type="search"
        aria-label="Search managed hosts"
        placeholder="Search managed hosts…"
        className={FIELD}
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          onChange('');
        }}
      />
      <select id="assign-host" className={FIELD} required value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Choose a managed host…</option>
        {assignable.map((h) => (
          <option key={h.id} value={h.id}>
            {h.name}
          </option>
        ))}
      </select>
      {hasNextPage && assignable.length > 0 && (
        <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
          <span>More managed hosts match. Search to narrow the list, or load more.</span>
          <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />
        </div>
      )}
      {exhausted && (
        <span className="text-[11px] text-muted-foreground">
          {q ? `No managed host matching “${q}” is left to assign.` : 'Every managed host is already a member.'}
        </span>
      )}
    </div>
  );
}
