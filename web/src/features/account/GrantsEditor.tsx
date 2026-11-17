import { Select } from '../../ui/Select.tsx';
import { useT } from '../../i18n/useT.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';
import type { GrantDraft, GrantRow } from './grants.ts';

/**
 * GrantsEditor is a personal token's organizations: one row per
 * organization the person belongs to, ticked to grant the token access
 * there, with the cap there (only roles up to the person's own role). An
 * organization where the person is blocked, or (for a new grant) where
 * personal tokens are turned off, is shown disabled with the reason.
 */
export function GrantsEditor({
  rows,
  value,
  onChange,
  error,
}: {
  rows: readonly GrantRow[];
  value: GrantDraft;
  onChange: (next: GrantDraft) => void;
  error?: string;
}) {
  const t = useT();
  const g = t.personalTokens.grants;

  function set(slug: string, patch: Partial<GrantDraft[string]>) {
    onChange({ ...value, [slug]: { ...value[slug], ...patch } });
  }

  return (
    <fieldset className="flex flex-col gap-1.5" aria-describedby="grants_hint">
      <legend className="mb-1.5 text-xs font-semibold">{g.legend}</legend>
      <span id="grants_hint" className="text-[11px] text-muted-foreground">
        {g.hint}
      </span>
      {rows.length === 0 && <span className="text-xs text-muted-foreground">{g.none}</span>}
      <ul className="flex flex-col rounded-md border border-border">
        {rows.map((row) => {
          const choice = value[row.slug] ?? { on: false, role: 'viewer' };
          const reasonId = `grant_reason_${row.slug}`;
          const reason =
            row.status === 'blocked' ? g.blocked : row.status === 'off' ? (row.existing ? g.offKept : g.off) : undefined;
          return (
            <li key={row.slug} data-grant-row="" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-2 last:border-b-0">
              <label className="flex min-w-0 flex-1 items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  aria-label={g.use(row.name)}
                  aria-describedby={reason ? reasonId : undefined}
                  checked={choice.on}
                  disabled={row.locked}
                  onChange={(e) => set(row.slug, { on: e.target.checked })}
                />
                <span aria-hidden="true" className="flex min-w-0 flex-col">
                  <span className="truncate font-semibold">{row.name}</span>
                  <span className="truncate text-[11px] text-muted-foreground">
                    {row.slug} · {g.yourRole(roleLabel(t, row.myRole))}
                  </span>
                </span>
              </label>
              <div className="w-36">
                <Select
                  aria-label={g.role(row.name)}
                  options={row.options.map((r) => ({ value: r, label: roleLabel(t, r) }))}
                  value={choice.role}
                  disabled={row.locked || !choice.on}
                  onChange={(next) => set(row.slug, { role: next as GrantDraft[string]['role'] })}
                />
              </div>
              {reason && (
                <span id={reasonId} className={`basis-full text-[11px] ${row.status === 'blocked' ? 'text-destructive' : 'text-muted-foreground'}`}>
                  {reason}
                  {row.status === 'blocked' && <span className="sr-only"> {g.blockedHint}</span>}
                </span>
              )}
            </li>
          );
        })}
      </ul>
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </fieldset>
  );
}
