import { Children, useId, type ReactNode } from 'react';

/**
 * NavGroup is one labelled section of the sidebar (v0.4f): a heading
 * over the entries that belong to it. The heading is a separator, not a
 * disclosure control — nothing collapses, so no page ever costs a second
 * click, and there is no open/closed state to remember.
 *
 * It renders nothing when every child is gated away. Three sidebar
 * entries are permission-gated, so a group can lose all of its links to
 * a role, and a heading left standing over nothing is worse than a
 * missing entry: it claims there is something there that the reader then
 * cannot find. `Children.toArray` drops the `false` that a
 * `{may(role, …) && <Link/>}` leaves behind, which is what makes the
 * count honest.
 *
 * Presentational only (src/ui, CLAUDE.md): it is handed its links, and
 * asks nobody who is signed in.
 */
export function NavGroup({ label, children }: { label: string; children: ReactNode }) {
  const headingId = useId();
  const items = Children.toArray(children);
  if (items.length === 0) return null;
  return (
    <div role="group" aria-labelledby={headingId} className="mt-4 flex flex-col gap-0.5 first:mt-0">
      <span
        id={headingId}
        className="px-3 pb-1 text-[10px] font-semibold tracking-wide text-muted-foreground uppercase"
      >
        {label}
      </span>
      {items}
    </div>
  );
}
