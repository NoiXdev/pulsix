import type { KeyboardEvent, MouseEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { cn } from '../../lib/utils.ts';
import type { MenuPoint } from '../menuPlacement.ts';
import { RowActionsButton } from './RowActionsMenu.tsx';
import type { DataListColumn, Density, RowAction, RowLink } from './types.ts';

/** A row's grid: the density's vertical padding and type size
 * (`--px-row-py`, `--px-row-font`, styles/globals.css). */
export const ROW_GRID = 'grid items-center gap-3 px-4 py-[var(--px-row-py)] text-[length:var(--px-row-font)]';

export type MenuSource = 'pointer' | 'keyboard';

/** What a row asks of its list to open its context menu. */
export type OpenRowMenu = (row: HTMLElement, point: MenuPoint, source: MenuSource, returnTo: HTMLElement | null) => void;

/**
 * DataRow is one body row of a DataList: `role="row"` of `role="cell"`s on
 * the list's grid. A linked row keeps ListRow's look (hover highlight and
 * left accent bar) and holds one link, in the link column's cell, whose
 * `::after` is stretched over the whole row — so middle-click, copy-link
 * and screen readers see one link named by the row's label. The "…" cell
 * sits above it (`relative z-10`), as must any link or button a cell
 * renders.
 */
export function DataRow<Row>({
  row,
  columns,
  template,
  density,
  link,
  linkColumn,
  menuColumn,
  actions,
  menuLabel,
  menuOpen,
  onOpenMenu,
}: {
  row: Row;
  columns: DataListColumn<Row>[];
  template: string;
  density: Density;
  link?: RowLink;
  linkColumn?: string;
  /** Whether the list reserves the last track for "…" buttons. */
  menuColumn: boolean;
  actions: RowAction[];
  menuLabel: string;
  /** This row's context menu is open. */
  menuOpen: boolean;
  onOpenMenu: OpenRowMenu;
}) {
  const hasActions = actions.length > 0;

  // The open "…" menu is portalled out of the row but is still its child in
  // React, so its key and pointer events bubble here: only events from the
  // row's own DOM count.
  function own(e: { currentTarget: HTMLElement; target: EventTarget }): boolean {
    return e.target instanceof Node && e.currentTarget.contains(e.target);
  }

  // A link a cell renders (Active runs) is its own link: its context menu
  // is the browser's, as "Open in new tab" in ours would open the row's page.
  function inCellLink(target: EventTarget): boolean {
    const a = target instanceof Element ? target.closest('a[href]') : null;
    return a !== null && !a.hasAttribute('data-row-link');
  }

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (!hasActions || !own(e) || inCellLink(e.target)) return;
    if (e.key !== 'ContextMenu' && !(e.shiftKey && e.key === 'F10')) return;
    e.preventDefault();
    const r = e.currentTarget.getBoundingClientRect();
    // Below the row; opening upwards it ends above the row, not over it.
    onOpenMenu(e.currentTarget, { x: r.left + 16, y: r.bottom, above: r.top }, 'keyboard', e.target as HTMLElement);
  }

  function onContextMenu(e: MouseEvent<HTMLDivElement>) {
    if (!hasActions || !own(e) || inCellLink(e.target)) return;
    // Shift+right-click is the way to the browser's own menu. (Shift+F10's
    // own contextmenu event is no right-click: it stays with ours.)
    if (e.shiftKey && e.button === 2) return;
    e.preventDefault();
    const returnTo = e.currentTarget.querySelector<HTMLElement>('[data-row-link], [data-row-menu]');
    onOpenMenu(e.currentTarget, { x: e.clientX, y: e.clientY }, 'pointer', returnTo);
  }

  return (
    <div
      role="row"
      data-list-row=""
      data-menu-open={menuOpen ? '' : undefined}
      onKeyDown={onKeyDown}
      onContextMenu={onContextMenu}
      style={{ gridTemplateColumns: template }}
      className={cn(
        'relative border-b border-border last:border-b-0',
        ROW_GRID,
        link && 'hover:bg-background hover:shadow-[inset_2px_0_0_var(--px-accent)] focus-within:bg-background',
        menuOpen && 'bg-background shadow-[inset_2px_0_0_var(--px-accent)]',
      )}
    >
      {columns.map((c) => {
        const content = c.render(row, { density });
        return (
          <div key={c.id} role="cell" className={cn('min-w-0', c.align === 'end' && 'text-right')}>
            {link && c.id === linkColumn ? (
              <Link
                to={link.to}
                params={link.params}
                search={link.search}
                hash={link.hash}
                aria-label={link.label}
                data-row-link=""
                className="block min-w-0 after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-primary focus-visible:after:ring-inset"
              >
                {content}
              </Link>
            ) : (
              content
            )}
          </div>
        );
      })}
      {menuColumn && (
        <div role="cell" className="relative z-10 flex justify-end">
          {hasActions && <RowActionsButton label={menuLabel} actions={actions} />}
        </div>
      )}
    </div>
  );
}
