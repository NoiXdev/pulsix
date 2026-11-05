import { useEffect, useState } from 'react';
import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { LoadMoreButton } from '../../../ui/LoadMoreButton.tsx';
import { useHosts } from '../../../api/hooks/useHosts.ts';
import { useT } from '../../../i18n/useT.ts';

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
  const t = useT();
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { managed: true, q });
  const assignable = (hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? []).filter((h) => !existing.includes(h.id));
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = hosts;

  // A loaded page may hold members only; fetch on until a candidate shows
  // up or the pages run out, so an empty list always means "none left".
  // A failed page fetch must stop this, not retry it forever (backlog
  // 2.20): isFetchNextPageError stays true — and hasNextPage stays true
  // too, since it is still computed from the last *successful* page —
  // until fetchNextPage is called again, which the effect itself must
  // never do on its own once a fetch has failed.
  useEffect(() => {
    if (hosts.data && assignable.length === 0 && hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage();
  }, [hosts.data, assignable.length, hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage]);

  const exhausted = !!hosts.data && !hasNextPage && assignable.length === 0 && !isFetchNextPageError;

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="assign-host">{t.inventory.picker.host}</Label>
      <input
        type="search"
        aria-label={t.inventory.picker.searchLabel}
        placeholder={t.inventory.picker.searchPlaceholder}
        className={FIELD}
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          onChange('');
        }}
      />
      <Select
        id="assign-host"
        required
        options={assignable.map((h) => ({ value: h.id, label: h.name }))}
        value={value}
        onChange={onChange}
        placeholder={t.inventory.picker.choose}
      />
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          {t.inventory.picker.loadFailed(hosts.error instanceof Error ? hosts.error.message : '')}
        </span>
      )}
      {isFetchNextPageError && (
        <div className="flex items-center justify-between gap-2 text-[11px]">
          <span role="alert" className="text-destructive">
            {t.inventory.picker.loadMoreFailed(hosts.error instanceof Error ? hosts.error.message : '')}
          </span>
          <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />
        </div>
      )}
      {!isFetchNextPageError && hasNextPage && assignable.length > 0 && (
        <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
          <span>{t.inventory.picker.more}</span>
          <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />
        </div>
      )}
      {exhausted && (
        <span className="text-[11px] text-muted-foreground">
          {q ? t.inventory.picker.noneMatching(q) : t.inventory.picker.allMembers}
        </span>
      )}
    </div>
  );
}
