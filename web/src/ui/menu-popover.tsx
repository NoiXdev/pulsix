import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { MenuContext } from './menuContext.ts';
import { placeAt, placeUnder, type MenuPoint, type Position } from './menuPlacement.ts';

export interface MenuPopoverProps {
  id?: string;
  /** The menu's accessible name. */
  label: string;
  /** The element the menu belongs to: the "…" trigger, or the row of a
   * context menu. A scroll that moves it, or a resize, closes the menu. */
  anchor: RefObject<HTMLElement | null>;
  /** Where to open (a right-click), in viewport coordinates. Absent: under
   * the anchor, right-aligned to it. With a point, a press on the anchor
   * counts as outside (it is a whole row); without, it does not (the
   * trigger toggles the menu itself). */
  point?: MenuPoint;
  /** `restoreFocus` is true for Escape, Tab and a picked item, false for a
   * click outside, a scroll or a resize. The owner moves focus back. */
  onClose: (restoreFocus: boolean) => void;
  children: ReactNode;
}

function viewport() {
  return { width: window.innerWidth, height: window.innerHeight };
}

/**
 * MenuPopover is the open state of a small action menu: a `role="menu"`
 * popover portalled to <body> with fixed positioning (a list sits in a Card
 * whose overflow-clip would cut off a menu opened near its bottom). Render
 * it only while the menu is open. It focuses its first item, moves between
 * items with the arrow keys, Home and End, and closes on Escape or Tab, on a
 * press outside it, and on a scroll or resize that would leave it detached
 * from its anchor. Items reach it through MenuContext (RowMenuItem,
 * RowMenuLink).
 */
export function MenuPopover({ id, label, anchor, point, onClose, children }: MenuPopoverProps) {
  const menuRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<Position | null>(null);
  // Where the anchor was when the menu was placed, to tell a scroll that
  // moved it from one that did not.
  const anchorAt = useRef<{ top: number; left: number } | null>(null);
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    onCloseRef.current = onClose;
  });
  const close = useCallback((restoreFocus: boolean) => onCloseRef.current(restoreFocus), []);
  const pointX = point?.x;
  const pointY = point?.y;
  const pointAbove = point?.above;
  const atPoint = point !== undefined;

  function items(): HTMLElement[] {
    return Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
  }

  // Measured before paint so it never shows at the wrong place.
  useLayoutEffect(() => {
    const a = anchor.current;
    if (!a || !menuRef.current) return;
    const menu = menuRef.current.getBoundingClientRect();
    const at = a.getBoundingClientRect();
    anchorAt.current = { top: at.top, left: at.left };
    setPos(
      pointX !== undefined && pointY !== undefined
        ? placeAt({ x: pointX, y: pointY, above: pointAbove }, menu.width, menu.height, viewport())
        : placeUnder(at, menu.width, menu.height, viewport()),
    );
  }, [anchor, pointX, pointY, pointAbove]);

  // Focus waits for the position: an element hidden by `visibility` cannot
  // take focus.
  const positioned = pos !== null;
  useEffect(() => {
    if (positioned) items()[0]?.focus();
  }, [positioned]);

  useEffect(() => {
    function onPointerDown(e: MouseEvent) {
      const target = e.target as Node;
      if (menuRef.current?.contains(target)) return;
      if (!atPoint && anchor.current?.contains(target)) return;
      close(false);
    }
    // A fixed popover does not follow its anchor, so a scroll (of the page or
    // of a scroll container) that moves the anchor, or a resize, closes it.
    // Scrolling the menu itself is not that, and neither is a scroll that
    // left the anchor where it was: the browser delivers scroll events on
    // its next frame, so the scroll that brought the anchor into view can
    // arrive after the click that opened the menu.
    function onScroll(e: Event) {
      if (e.target instanceof Node && menuRef.current?.contains(e.target)) return;
      const was = anchorAt.current;
      const now = anchor.current?.getBoundingClientRect();
      if (was && now && now.top === was.top && now.left === was.left) return;
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
  }, [anchor, atPoint, close]);

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    // The menu is portalled to the end of <body>, so letting Tab move on
    // from here would send focus past the whole page. It goes back to where
    // the menu came from instead, and the next Tab continues from there.
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

  return createPortal(
    <MenuContext.Provider value={{ select: () => close(true) }}>
      <div
        ref={menuRef}
        id={id}
        role="menu"
        aria-label={label}
        onKeyDown={onKeyDown}
        // A context menu opened from the keyboard has focus by the time the
        // browser sends the key's own contextmenu event: its native menu
        // must not open over this one. A real right-click on a link item
        // still gets the browser's menu (save a download, open in a tab),
        // and a "…" menu is left alone entirely.
        onContextMenu={
          atPoint
            ? (e) => {
                const onLink = e.button === 2 && e.target instanceof Element && e.target.closest('a[href]') !== null;
                if (!onLink) e.preventDefault();
              }
            : undefined
        }
        style={{ position: 'fixed', top: pos?.top ?? 0, left: pos?.left ?? 0, visibility: pos ? 'visible' : 'hidden' }}
        className="z-50 flex min-w-40 flex-col gap-0.5 rounded-md border border-border bg-card p-1 shadow-lg"
      >
        {children}
      </div>
    </MenuContext.Provider>,
    document.body,
  );
}

/** A thin line between groups of menu items; the arrow keys skip it. */
export function MenuSeparator() {
  return <div role="separator" className="mx-1 my-0.5 h-px bg-border" />;
}
