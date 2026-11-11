import { useState } from 'react';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useT } from '../../i18n/useT.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';

// The search <input> stays a plain text field rather than becoming a
// Select.
const SEARCH = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';

/**
 * HostPicker chooses the monitored host a host-bound definition applies
 * to. The hosts list is paginated, so — like inventory's
 * ManagedHostPicker — it searches through the API (`q`) rather than
 * filtering one page, and says plainly when more hosts match than are
 * listed instead of pretending the first page is the fleet.
 *
 * It is rendered only while the binding is "host", so choosing any other
 * binding costs no request at all.
 *
 * Whatever that search answers, the host this definition is ALREADY bound
 * to stays in the list as its own option: one page of results cannot be
 * assumed to contain it (a fleet paginated past it, a host no longer
 * monitored, a search the operator has since narrowed), and a select with
 * no option matching its value silently falls back to the empty
 * placeholder — the dialog would then state the binding was unset and
 * refuse to submit, with no message anywhere (review finding 1). For the
 * same reason typing in the search box no longer clears the selection.
 */
export function HostPicker({
  org, value, currentName, onChange,
}: {
  org: string;
  value: string;
  /** The bound host's name as the definition itself reports it
   * (host_name), when this is an edit of an existing host binding. */
  currentName?: string;
  onChange: (id: string) => void;
}) {
  const t = useT();
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="check-host">{t.checks.host}</Label>
      <input
        type="search"
        aria-label={t.checks.hostPicker.searchLabel}
        placeholder={t.checks.hostPicker.searchPlaceholder}
        className={SEARCH}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Select
        id="check-host"
        placeholder={t.checks.hostPicker.choose}
        required
        value={value}
        onChange={onChange}
        options={[
          ...(currentMissing ? [{ value, label: t.checks.currentBinding(currentName ?? value) }] : []),
          ...listed.map((h) => ({ value: h.id, label: h.name })),
        ]}
      />
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">{t.checks.hostPicker.moreMatch}</span>}
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          {t.checks.hostPicker.loadFailed} {hosts.error instanceof Error ? hosts.error.message : ''}
        </span>
      )}
    </div>
  );
}
