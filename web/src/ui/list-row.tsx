import { useCallback, useId, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { Link, type LinkProps } from '@tanstack/react-router';
import { MoreHorizontal } from 'lucide-react';
import { cn } from '../lib/utils.ts';
import { MenuPopover } from './menu-popover.tsx';
import { useMenu } from './menuContext.ts';

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
 * name. The cell that shows the name goes in ListRowName, which keeps it out of
 * that description, so the name is read once. The hover highlight is
 * `bg-background`, not `bg-card`: a list sits in a
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
 * ListRowName is the cell of a ListRow that shows the row's name. It is hidden
 * from assistive technology: the row's `label` already names the link, and
 * without this the name would be read again as part of the description.
 */
export function ListRowName({ className, style, children }: { className?: string; style?: CSSProperties; children: ReactNode }) {
  return (
    <span aria-hidden="true" className={className} style={style}>
      {children}
    </span>
  );
}

/**
 * RowMenu is a small popover menu for a row's secondary actions. The trigger
 * swallows its click (preventDefault and stopPropagation) so a menu placed
 * over a row never also activates the row. The menu itself is MenuPopover:
 * it closes on Escape or Tab (focus returns to the trigger), on a click
 * outside it and after an item is selected; the arrow keys, Home and End
 * move between the items. `triggerClassName` replaces the trigger's size and
 * look where the menu sits beside full-size buttons rather than in a row.
 */
export function RowMenu({ label, triggerClassName, children }: { label: string; triggerClassName?: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();

  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  }, []);

  return (
    <div className="relative">
      <button
        ref={triggerRef}
        type="button"
        data-row-menu=""
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
        className={cn(
          'flex items-center justify-center focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
          triggerClassName ?? 'h-7 w-7 rounded-md text-muted-foreground hover:bg-background hover:text-foreground',
        )}
      >
        <MoreHorizontal size={16} />
      </button>
      {open && (
        <MenuPopover id={menuId} label={label} anchor={triggerRef} onClose={close}>
          {children}
        </MenuPopover>
      )}
    </div>
  );
}

/** One action in a RowMenu. `destructive` colours it as a delete-like one.
 * `disabled` dims it and ignores a pick, which leaves the menu open. It stays
 * focusable (aria-disabled, not the attribute), as the menu pattern keeps a
 * disabled item reachable by the arrow keys so its label can be read. */
export function RowMenuItem({
  onSelect,
  destructive,
  disabled,
  children,
}: {
  onSelect: () => void;
  destructive?: boolean;
  disabled?: boolean;
  children: ReactNode;
}) {
  const menu = useMenu();
  return (
    <button
      type="button"
      role="menuitem"
      tabIndex={-1}
      aria-disabled={disabled ? true : undefined}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        if (disabled) return;
        menu?.select();
        onSelect();
      }}
      className={cn(
        'rounded px-2.5 py-1.5 text-left text-xs focus-visible:outline-none',
        disabled && 'cursor-not-allowed opacity-50',
        destructive
          ? 'text-destructive hover:bg-destructive/10 focus-visible:bg-destructive/10'
          : 'text-foreground hover:bg-background focus-visible:bg-background',
      )}
    >
      {children}
    </button>
  );
}

/** A RowMenu entry that is a link — a download, or a page of its own — so the
 * browser treats it as one (save-as, open in a new tab). Picking it closes
 * the menu. */
export function RowMenuLink({ href, download, children }: { href: string; download?: string; children: ReactNode }) {
  const menu = useMenu();
  return (
    <a
      role="menuitem"
      tabIndex={-1}
      href={href}
      download={download}
      onClick={(e) => {
        e.stopPropagation();
        menu?.select();
      }}
      className="rounded px-2.5 py-1.5 text-left text-xs text-foreground hover:bg-background focus-visible:bg-background focus-visible:outline-none"
    >
      {children}
    </a>
  );
}
