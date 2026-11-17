import { useCallback, useRef, useState, type ReactNode } from 'react';
import { useT } from '../../i18n/useT.ts';
import type { MenuPoint } from '../menuPlacement.ts';
import { Card } from '../table.tsx';
import { CELL_MINOR } from './cellText.ts';
import { ColumnsMenu } from './ColumnsMenu.tsx';
import { DataRow, ROW_GRID, type MenuSource, type OpenRowMenu } from './DataRow.tsx';
import { Pager } from './Pager.tsx';
import { RowContextMenu } from './RowActionsMenu.tsx';
import { SortHeader } from './SortHeader.tsx';
import type { DataListColumn, DataListProps } from './types.ts';

/** The track reserved for the "…" buttons when the list has row actions. */
const MENU_TRACK = '40px';

interface ContextMenuState {
  key: string;
  point: MenuPoint;
  source: MenuSource;
  /** Tells a reopened menu from the one before, so it is placed afresh. */
  seq: number;
}

/**
 * DataList is the one list component: an ARIA table on the list's grid
 * columns, inside a Card, with a toolbar row above it (the caller's search
 * and filters, then the Columns button) and, when paged, a footer.
 *
 * It is presentational: sort, page, page size, shown columns and density
 * come in as props with their change handlers (features/lists holds them).
 * Columns are rendered in the order of `visibleColumns`; sortable headers
 * are buttons that step none → ascending → descending → none. A row with
 * actions has a "…" menu and the same menu on a right-click, the
 * ContextMenu key or Shift+F10, which for a linked row also offers "Open in
 * new tab" and "Copy link".
 */
export function DataList<Row>({
  label,
  columns,
  rows,
  rowKey,
  rowLink,
  rowLabel,
  rowActions,
  sort,
  onSortChange,
  paging,
  visibleColumns,
  onVisibleColumnsChange,
  defaultColumns,
  density,
  loading,
  empty,
  toolbar,
}: DataListProps<Row>) {
  const t = useT();
  const shown = shownColumns(columns, visibleColumns);
  const menuColumn = rowActions !== undefined;
  const template = [...shown.map((c) => c.width), ...(menuColumn ? [MENU_TRACK] : [])].join(' ');
  const linkColumn = (shown.find((c) => c.required) ?? shown[0])?.id;

  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const anchorRef = useRef<HTMLElement | null>(null);
  const returnRef = useRef<HTMLElement | null>(null);
  const seq = useRef(0);

  const closeMenu = useCallback((restoreFocus: boolean) => {
    setMenu(null);
    if (restoreFocus) returnRef.current?.focus();
  }, []);

  const openMenu = (key: string): OpenRowMenu => (rowEl, point, source, returnTo) => {
    // The ContextMenu key and Shift+F10 also make the browser send a
    // contextmenu event; it must not move the menu just opened for them.
    if (source === 'pointer' && menu?.key === key && menu.source === 'keyboard') return;
    anchorRef.current = rowEl;
    returnRef.current = returnTo;
    seq.current += 1;
    setMenu({ key, point, source, seq: seq.current });
  };

  const menuRow = menu ? rows.find((r) => rowKey(r) === menu.key) : undefined;
  // The menu's row went away (a refetch while it was open): the menu is
  // gone with it, and must not come back if the row does.
  if (menu && menuRow === undefined) setMenu(null);
  const nameOf = (row: Row) => rowLabel?.(row) ?? rowLink?.(row)?.label;
  const menuLabel = (row: Row) => {
    const name = nameOf(row);
    return name ? t.ui.dataList.actionsFor(name) : t.ui.dataList.actions;
  };

  let body: ReactNode;
  if (rows.length > 0) {
    body = rows.map((row) => {
      const key = rowKey(row);
      return (
        <DataRow
          key={key}
          row={row}
          columns={shown}
          template={template}
          density={density}
          link={rowLink?.(row)}
          linkColumn={linkColumn}
          menuColumn={menuColumn}
          actions={rowActions?.(row) ?? []}
          menuLabel={menuLabel(row)}
          menuOpen={menu?.key === key && menuRow !== undefined}
          onOpenMenu={openMenu(key)}
        />
      );
    });
  } else {
    body = (
      <div role="row" className={ROW_GRID}>
        <div role="cell" className={`${CELL_MINOR} text-muted-foreground`}>
          {loading ? t.common.loading : empty}
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{toolbar}</div>
        <ColumnsMenu
          columns={columns}
          visibleColumns={visibleColumns}
          onVisibleColumnsChange={onVisibleColumnsChange}
          defaultColumns={defaultColumns}
        />
      </div>
      <Card>
        <div role="table" aria-label={label} aria-busy={loading ? true : undefined}>
          <div role="rowgroup">
            <div
              role="row"
              style={{ gridTemplateColumns: template }}
              className="grid items-center gap-3 border-b border-border px-4 py-2.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase"
            >
              {shown.map((c) => (
                <SortHeader key={c.id} column={c} sort={sort} onSortChange={onSortChange} />
              ))}
              {menuColumn && (
                <div role="columnheader">
                  <span className="sr-only">{t.ui.dataList.actions}</span>
                </div>
              )}
            </div>
          </div>
          <div role="rowgroup">{body}</div>
        </div>
        {paging && paging.total > 0 && <Pager {...paging} />}
      </Card>
      {menu && menuRow !== undefined && (
        <RowContextMenu
          key={menu.seq}
          label={menuLabel(menuRow)}
          anchor={anchorRef}
          point={menu.point}
          link={rowLink?.(menuRow)}
          actions={rowActions?.(menuRow) ?? []}
          onClose={closeMenu}
        />
      )}
    </div>
  );
}

/** The columns to show, in the order of `visible`; unknown and repeated ids
 * are skipped. */
function shownColumns<Row>(columns: DataListColumn<Row>[], visible: string[]): DataListColumn<Row>[] {
  const byId = new Map(columns.map((c) => [c.id, c]));
  const seen = new Set<string>();
  const out: DataListColumn<Row>[] = [];
  for (const id of visible) {
    const c = byId.get(id);
    if (!c || seen.has(id)) continue;
    seen.add(id);
    out.push(c);
  }
  return out;
}
