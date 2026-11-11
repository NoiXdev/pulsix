export interface Rect {
  top: number;
  bottom: number;
  left: number;
  width: number;
  height: number;
}

export interface Placement {
  top: number;
  left: number;
  side: 'below' | 'above';
}

/** Space kept between the field and its popover, and between the popover
 * and the viewport's edges. */
const GAP = 4;
const EDGE = 8;

/**
 * Where a popover goes for a field, in viewport coordinates (the popover is
 * `position: fixed`, which is also what keeps a scrolling dialog's overflow
 * from clipping it). Below the field when it fits there or when there is at
 * least as much room below as above; above otherwise. Kept inside the
 * viewport horizontally. A zero-sized world (jsdom, which does no layout)
 * still gets a finite answer: below, at the viewport's edge margin.
 */
export function placePopover(anchor: Rect, popover: Pick<Rect, 'width' | 'height'>, viewport: { width: number; height: number }): Placement {
  const roomBelow = viewport.height - anchor.bottom - GAP - EDGE;
  const roomAbove = anchor.top - GAP - EDGE;
  const below = popover.height <= roomBelow || roomBelow >= roomAbove;
  const top = below ? anchor.bottom + GAP : Math.max(EDGE, anchor.top - GAP - popover.height);
  const maxLeft = viewport.width - popover.width - EDGE;
  const left = Math.max(EDGE, Math.min(anchor.left, maxLeft));
  return { top, left, side: below ? 'below' : 'above' };
}
