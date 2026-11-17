import { useCallback, useEffect, useId, useMemo, useRef, useState, type DragEvent, type ReactNode } from 'react';
import { ArrowDown, ArrowUp, Columns3, GripVertical } from 'lucide-react';
import { useT } from '../../i18n/useT.ts';
import { cn } from '../../lib/utils.ts';
import { buttonClass } from '../buttonStyles.ts';
import { Popover } from '../Popover.tsx';
import type { DataListColumn } from './types.ts';

export interface ColumnsMenuProps<Row> {
  columns: DataListColumn<Row>[];
  visibleColumns: string[];
  onVisibleColumnsChange: (ids: string[]) => void;
  defaultColumns: string[];
}

/** A column's name in the menu: its label, or for an icon column its
 * menuLabel. */
function columnName<Row>(c: DataListColumn<Row>): string {
  return c.label || c.menuLabel || c.id;
}

/** The menu's order for a set of shown columns: the required column, the
 * shown ones in their order, then the hidden ones in definition order. */
function initialOrder<Row>(columns: DataListColumn<Row>[], shown: Set<string>, visible: string[]): string[] {
  const required = columns.filter((c) => c.required).map((c) => c.id);
  const shownInOrder = visible.filter((id) => shown.has(id) && !required.includes(id));
  const hidden = columns.filter((c) => !c.required && !shown.has(c.id)).map((c) => c.id);
  return [...required, ...shownInOrder, ...hidden];
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/**
 * ColumnsMenu is the "Columns" button above a list and its popover: one
 * checkbox per column in display order (the required one checked and
 * locked first), up and down buttons and drag and drop to reorder, and
 * "Restore defaults". Every change reports the new shown columns at once —
 * a drag only when it is dropped.
 */
export function ColumnsMenu<Row>({ columns, visibleColumns, onVisibleColumnsChange, defaultColumns }: ColumnsMenuProps<Row>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const popoverId = useId();
  const close = useCallback((reason: 'escape' | 'outside') => {
    setOpen(false);
    if (reason === 'escape') buttonRef.current?.focus();
  }, []);
  const known = new Set(columns.map((c) => c.id));
  const shownCount = new Set(visibleColumns.filter((id) => known.has(id))).size;

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? popoverId : undefined}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key !== 'Escape' || !open) return;
          e.preventDefault();
          close('escape');
        }}
        className={buttonClass('default', 'h-[30px] gap-1.5 px-2.5 text-xs font-medium')}
      >
        <Columns3 size={14} aria-hidden="true" />
        {t.ui.dataList.columns}
        <span aria-hidden="true" className="font-mono text-[11px] font-normal text-muted-foreground">
          {shownCount}/{columns.length}
        </span>
      </button>
      {open && (
        <Popover anchor={buttonRef} onClose={close} id={popoverId} label={t.ui.dataList.columns}>
          <ColumnsPanel
            columns={columns}
            visibleColumns={visibleColumns}
            onVisibleColumnsChange={onVisibleColumnsChange}
            defaultColumns={defaultColumns}
          />
        </Popover>
      )}
    </>
  );
}

function ColumnsPanel<Row>({ columns, visibleColumns, onVisibleColumnsChange, defaultColumns }: ColumnsMenuProps<Row>) {
  const t = useT();
  const byId = useMemo(() => new Map(columns.map((c) => [c.id, c])), [columns]);
  const isRequired = useCallback((id: string) => byId.get(id)?.required === true, [byId]);
  const shown = useMemo(
    () => new Set(visibleColumns.filter((id) => byId.has(id)).concat(columns.filter((c) => c.required).map((c) => c.id))),
    [visibleColumns, columns, byId],
  );
  // The menu's own order, hidden columns included. Kept while it matches
  // what the list shows, so a hidden column keeps the place it was moved to
  // while the menu is open; rebuilt when the shown columns change from
  // elsewhere (Restore defaults, another tab).
  const [local, setLocal] = useState<string[] | null>(null);
  const order = useMemo(() => {
    const fresh = initialOrder(columns, shown, visibleColumns);
    if (!local || local.length !== fresh.length || !fresh.every((id) => local.includes(id))) return fresh;
    const localShown = local.filter((id) => shown.has(id));
    return sameList(localShown, fresh.filter((id) => shown.has(id))) ? local : fresh;
  }, [local, columns, shown, visibleColumns]);
  const shownList = order.filter((id) => shown.has(id));

  // Opening moves focus into the panel, so the keyboard lands on the first
  // column it can change and Escape reaches the popover.
  const panelRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    panelRef.current?.querySelector<HTMLElement>('input:not(:disabled), button:not(:disabled)')?.focus();
  }, []);

  const dragId = useRef<string | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  function commit(nextOrder: string[], nextShown: Set<string>) {
    setLocal(nextOrder);
    const next = nextOrder.filter((id) => isRequired(id) || nextShown.has(id));
    if (!sameList(next, shownList)) onVisibleColumnsChange(next);
  }

  function toggle(id: string, on: boolean) {
    const next = new Set(shown);
    if (on) next.add(id);
    else next.delete(id);
    commit(order, next);
  }

  /** Moves `id` to index `to`, never above the required column. */
  function moveTo(id: string, to: number) {
    const from = order.indexOf(id);
    const floor = order.filter(isRequired).length;
    if (from < 0 || isRequired(id) || to < floor || to >= order.length || to === from) return;
    const next = order.filter((v) => v !== id);
    next.splice(to, 0, id);
    commit(next, shown);
  }

  function onDragStart(e: DragEvent<HTMLLIElement>, id: string) {
    dragId.current = id;
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', id);
    }
  }

  function canDropOn(id: string): boolean {
    return dragId.current !== null && dragId.current !== id && !isRequired(id);
  }

  function onDragOver(e: DragEvent<HTMLLIElement>, id: string) {
    if (!canDropOn(id)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
    if (dropTarget !== id) setDropTarget(id);
  }

  function onDrop(e: DragEvent<HTMLLIElement>, id: string) {
    e.preventDefault();
    const dragged = dragId.current;
    endDrag();
    if (dragged && dragged !== id && !isRequired(id)) moveTo(dragged, order.indexOf(id));
  }

  function endDrag() {
    dragId.current = null;
    setDropTarget(null);
  }

  const floor = order.filter(isRequired).length;
  return (
    <div ref={panelRef} className="flex w-64 flex-col gap-2">
      <p className="text-[11px] leading-snug text-muted-foreground">{t.ui.dataList.columnsHint}</p>
      <ul className="flex flex-col">
        {order.map((id, i) => {
          const c = byId.get(id);
          if (!c) return null;
          const name = columnName(c);
          const required = c.required === true;
          return (
            <li
              key={id}
              draggable={!required}
              onDragStart={(e) => onDragStart(e, id)}
              onDragOver={(e) => onDragOver(e, id)}
              onDragLeave={() => dropTarget === id && setDropTarget(null)}
              onDrop={(e) => onDrop(e, id)}
              onDragEnd={endDrag}
              className={cn(
                'flex items-center gap-1.5 rounded px-1 py-1 text-xs text-foreground hover:bg-background',
                dropTarget === id && 'bg-background shadow-[inset_0_2px_0_var(--px-accent)]',
              )}
            >
              <GripVertical size={12} aria-hidden="true" className={cn('shrink-0 text-muted-foreground', required ? 'invisible' : 'cursor-grab')} />
              <label className="flex min-w-0 flex-1 items-center gap-2">
                <input type="checkbox" checked={shown.has(id)} disabled={required} onChange={(e) => toggle(id, e.target.checked)} />
                <span className="truncate">{name}</span>
              </label>
              {required ? (
                <span className="pr-1 text-[10.5px] text-muted-foreground">{t.ui.dataList.alwaysShown}</span>
              ) : (
                <span className="flex shrink-0 items-center">
                  <MoveButton label={t.ui.dataList.moveUp(name)} disabled={i <= floor} onClick={() => moveTo(id, i - 1)}>
                    <ArrowUp size={12} aria-hidden="true" />
                  </MoveButton>
                  <MoveButton label={t.ui.dataList.moveDown(name)} disabled={i >= order.length - 1} onClick={() => moveTo(id, i + 1)}>
                    <ArrowDown size={12} aria-hidden="true" />
                  </MoveButton>
                </span>
              )}
            </li>
          );
        })}
      </ul>
      <div className="flex border-t border-border pt-2">
        <button
          type="button"
          onClick={() => {
            setLocal(null);
            if (!sameList(defaultColumns, visibleColumns)) onVisibleColumnsChange([...defaultColumns]);
          }}
          className="text-xs text-brand hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          {t.ui.dataList.restoreDefaults}
        </button>
      </div>
    </div>
  );
}

function MoveButton({ label, disabled, onClick, children }: { label: string; disabled: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-card hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-30"
    >
      {children}
    </button>
  );
}
