import { useState } from 'react';
import { Label } from '../../ui/label.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { useHost, useHosts } from '../../api/hooks/useHosts.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * HostChecklist is a paginated, searchable multi-select of managed hosts:
 * every managed host stays reachable (the same "search past the loaded
 * page" contract ManagedHostPicker uses for the single-select case). The
 * ad-hoc command dialog and the playbook's dry run choose their hosts with
 * it.
 *
 * A selected host that is not on the loaded page — a preselected one that
 * sorts past the first page, or one picked before the search changed — is
 * pinned above the list as a checked row, its name read with useHost, so
 * the selection that will be sent is always on screen.
 */
export function HostChecklist({
  org,
  id,
  label,
  selected,
  onToggle,
}: {
  org: string;
  /** The search field's id, which the label names. */
  id: string;
  label: string;
  selected: readonly string[];
  onToggle: (hostId: string) => void;
}) {
  const t = useT();
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { managed: true, q }, { keepPrevious: true });
  const page = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  // Only once the page is known: before that every selection would be
  // pinned and then jump into the list as the page arrives.
  const pinned = hosts.data ? selected.filter((h) => !page.some((x) => x.id === h)) : [];
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = hosts;

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <input
        id={id}
        type="search"
        aria-label={t.runs.adhoc.searchLabel}
        placeholder={t.runs.adhoc.searchPlaceholder}
        className="h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <div className="flex max-h-48 flex-col gap-1 overflow-y-auto rounded-md border border-border p-2">
        {pinned.map((h) => (
          <PinnedHostRow key={h} org={org} id={h} onToggle={() => onToggle(h)} />
        ))}
        {page.map((h) => (
          <label key={h.id} className="flex items-center gap-2 text-xs">
            <input type="checkbox" checked={selected.includes(h.id)} onChange={() => onToggle(h.id)} aria-label={h.name} />
            {h.name}
          </label>
        ))}
        {hosts.data && page.length === 0 && (
          <span className="text-[11px] text-muted-foreground">{q ? t.runs.adhoc.noMatch(q) : t.runs.adhoc.noHosts}</span>
        )}
      </div>
      {hasNextPage && (
        <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
          <span>{t.runs.adhoc.more}</span>
          <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />
        </div>
      )}
      <span className="text-[11px] text-muted-foreground">{t.runs.adhoc.selected(selected.length)}</span>
    </div>
  );
}

/** PinnedHostRow is one selected host that the loaded page does not carry:
 * checked, and unchecking it drops it from the selection (and so from
 * view). Its name comes from GET /hosts/{id}; the id stands in until then. */
function PinnedHostRow({ org, id, onToggle }: { org: string; id: string; onToggle: () => void }) {
  const host = useHost(org, id);
  const name = host.data?.host.name ?? id;
  return (
    <label className="flex items-center gap-2 text-xs">
      <input type="checkbox" checked onChange={onToggle} aria-label={name} />
      {name}
    </label>
  );
}
