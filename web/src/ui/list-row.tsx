import {
  createContext,
  useContext,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { Link, type LinkProps } from '@tanstack/react-router';
import { MoreHorizontal } from 'lucide-react';
import { cn } from '../lib/utils.ts';

/** What a list row looks like, linked or not: separated by a hairline, laid
 * out on the list's grid columns. */
const ROW_FRAME = 'border-b border-border last:border-b-0';
const ROW_GRID = 'grid items-center gap-3 px-4 py-2.5';

/**
 * ListRow is one row of a list whose whole surface opens a detail page: a
 * real <a> (so middle-click, copy-link and screen readers work) laid out on
 * the given grid columns, with a hover highlight and a left accent bar.
 *
 * `label` is the link's accessible name, rendered as a visually hidden span the
 * link points at with `aria-labelledby`; the cells are its accessible
 * description, so a screen reader still reads them without them becoming the
 * name. The hover highlight is `bg-background`, not `bg-card`: a list sits in a
 * Card, whose own surface is `bg-card`. The optional `menu` is rendered as a sibling of
 * the anchor, positioned over the row's last cell, because a button nested
 * in an <a> is invalid HTML and not reliably operable. Reserve that last
 * column (about 40px) in `columns` and leave its cell empty.
 */
export function ListRow({
  to,
  params,
  search,
  hash,
  columns,
  label,
  children,
  menu,
}: {
  to: LinkProps['to'];
  params: LinkProps['params'];
  search?: LinkProps['search'];
  hash?: LinkProps['hash'];
  columns: string;
  label: string;
  children: ReactNode;
  menu?: ReactNode;
}) {
  const nameId = useId();
  const detailsId = useId();
  return (
    <div data-list-row="" className={cn('relative', ROW_FRAME, 'hover:bg-background hover:shadow-[inset_2px_0_0_var(--px-accent)] focus-within:bg-background')}>
      <Link
        to={to}
        params={params}
        search={search}
        hash={hash}
        aria-labelledby={nameId}
        aria-describedby={detailsId}
        className={cn(ROW_GRID, 'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary')}
        style={{ gridTemplateColumns: columns }}
      >
        <span id={nameId} className="sr-only">
          {label}
        </span>
        <div id={detailsId} className="contents">
          {children}
        </div>
      </Link>
      {menu && <div className="absolute top-1/2 right-3 -translate-y-1/2">{menu}</div>}
    </div>
  );
}

/**
 * StaticListRow is a row of the same list that opens nothing: the same
 * columns and spacing as ListRow, no link, no hover highlight (it would
 * promise a click). `title`, when given, is shown on hover, to say why it
 * cannot be opened. The optional `menu` sits over the row's last cell exactly
 * as in ListRow: reserve that last column (about 40px) in `columns` and leave
 * its cell empty.
 */
export function StaticListRow({
  columns,
  title,
  children,
  menu,
}: {
  columns: string;
  title?: string;
  children: ReactNode;
  menu?: ReactNode;
}) {
  return (
    <div data-list-row="" className={cn('relative', ROW_FRAME)}>
      <div title={title} className={ROW_GRID} style={{ gridTemplateColumns: columns }}>
        {children}
      </div>
      {menu && <div className="absolute top-1/2 right-3 -translate-y-1/2">{menu}</div>}
    </div>
  );
}

interface MenuContextValue {
  /** Closes the menu and puts focus back on its trigger. */
  select: () => void;
}

/** Gap between the trigger and the popover, and the minimum distance the
 * popover keeps from the viewport edges. */
const GAP = 4;
const EDGE = 8;

interface Position {
  top: number;
  left: number;
}

/** Places a popover of the given size under the trigger, right-aligned to it,
 * flipping above when it would cross the viewport bottom and there is room. */
function place(trigger: DOMRect, width: number, height: number): Position {
  const below = trigger.bottom + GAP;
  const fitsBelow = below + height <= window.innerHeight - EDGE;
  const above = trigger.top - GAP - height;
  const top = fitsBelow || above < EDGE ? below : above;
  const left = Math.max(EDGE, Math.min(trigger.right - width, window.innerWidth - width - EDGE));
  return { top, left };
}

const MenuContext = createContext<MenuContextValue | null>(null);

/**
 * RowMenu is a small popover menu for a row's secondary actions. The trigger
 * swallows its click (preventDefault and stopPropagation) so a menu placed
 * over a row never also activates the row. The menu closes on Escape or Tab (focus
 * returns to the trigger), on a click outside it and after an item is
 * selected; the arrow keys, Home and End move between the items.
 */
export function RowMenu({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const [pos, setPos] = useState<Position | null>(null);

  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false);
    setPos(null);
    if (restoreFocus) triggerRef.current?.focus();
  }, []);

  function items(): HTMLElement[] {
    return Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
  }

  // The popover is portalled to <body> with fixed positioning: a list sits in
  // a Card whose overflow-clip would cut off a menu opened near its bottom.
  // Measured before paint so it never shows at the wrong place.
  useLayoutEffect(() => {
    if (!open || !triggerRef.current || !menuRef.current) return;
    const menu = menuRef.current.getBoundingClientRect();
    setPos(place(triggerRef.current.getBoundingClientRect(), menu.width, menu.height));
  }, [open]);

  // Focus waits for the position: an element hidden by `visibility` cannot
  // take focus.
  const positioned = pos !== null;
  useEffect(() => {
    if (open && positioned) items()[0]?.focus();
  }, [open, positioned]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: MouseEvent) {
      const target = e.target as Node;
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) close(false);
    }
    // A fixed popover does not follow its row, so any scroll (of the page or
    // of a scroll container) or a resize closes it. Scrolling the menu itself
    // is not that.
    function onScroll(e: Event) {
      if (e.target instanceof Node && menuRef.current?.contains(e.target)) return;
      close(false);
    }
    function onResize() {
      close(false);
    }
    document.addEventListener('mousedown', onPointerDown);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  }, [open, close]);

  function onMenuKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    // The menu is portalled to the end of <body>, so letting Tab move on
    // from here would send focus past the whole page. It goes back to the
    // trigger instead, and the next Tab continues from the row.
    if (e.key === 'Tab') {
      e.preventDefault();
      close(true);
      return;
    }
    const list = items();
    const at = list.indexOf(document.activeElement as HTMLElement);
    let next: number | undefined;
    if (e.key === 'ArrowDown') next = (at + 1) % list.length;
    else if (e.key === 'ArrowUp') next = (at - 1 + list.length) % list.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = list.length - 1;
    if (next === undefined) return;
    e.preventDefault();
    list[next]?.focus();
  }

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={triggerRef}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          if (open) close(false);
          else setOpen(true);
        }}
        className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <MoreHorizontal size={16} />
      </button>
      {open &&
        createPortal(
          <MenuContext.Provider value={{ select: () => close(true) }}>
            <div
              ref={menuRef}
              id={menuId}
              role="menu"
              aria-label={label}
              onKeyDown={onMenuKeyDown}
              style={{ position: 'fixed', top: pos?.top ?? 0, left: pos?.left ?? 0, visibility: pos ? 'visible' : 'hidden' }}
              className="z-50 flex min-w-40 flex-col gap-0.5 rounded-md border border-border bg-card p-1 shadow-lg"
            >
              {children}
            </div>
          </MenuContext.Provider>,
          document.body,
        )}
    </div>
  );
}

/** One action in a RowMenu. `destructive` colours it as a delete-like one. */
export function RowMenuItem({
  onSelect,
  destructive,
  children,
}: {
  onSelect: () => void;
  destructive?: boolean;
  children: ReactNode;
}) {
  const menu = useContext(MenuContext);
  return (
    <button
      type="button"
      role="menuitem"
      tabIndex={-1}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        menu?.select();
        onSelect();
      }}
      className={cn(
        'rounded px-2.5 py-1.5 text-left text-xs focus-visible:outline-none',
        destructive
          ? 'text-destructive hover:bg-destructive/10 focus-visible:bg-destructive/10'
          : 'text-foreground hover:bg-background focus-visible:bg-background',
      )}
    >
      {children}
    </button>
  );
}
