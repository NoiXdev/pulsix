/** Gap between the trigger and the popover, and the minimum distance the
 * popover keeps from the viewport edges. */
const GAP = 4;
const EDGE = 8;

export interface Position {
  top: number;
  left: number;
}

export interface Viewport {
  width: number;
  height: number;
}

/** Where a context menu opens: its top-left corner at (x, y). `above` is
 * where its bottom goes when it has to open upwards — the pointer by
 * default, the row's top for a menu opened from the keyboard below its row,
 * so the flipped menu does not cover the row. */
export interface MenuPoint {
  x: number;
  y: number;
  above?: number;
}

/** Places a popover of the given size under the trigger, right-aligned to it,
 * flipping above when it would cross the viewport bottom and there is room. */
export function placeUnder(trigger: DOMRect, width: number, height: number, view: Viewport): Position {
  const below = trigger.bottom + GAP;
  const fitsBelow = below + height <= view.height - EDGE;
  const above = trigger.top - GAP - height;
  const top = fitsBelow || above < EDGE ? below : above;
  const left = Math.max(EDGE, Math.min(trigger.right - width, view.width - width - EDGE));
  return { top, left };
}

/** Places a popover at a point, opening upwards (ending at `above`) when it
 * would cross the viewport bottom, and kept inside the viewport. */
export function placeAt(point: MenuPoint, width: number, height: number, view: Viewport): Position {
  const fitsBelow = point.y + height <= view.height - EDGE;
  const top = fitsBelow ? point.y : Math.max(EDGE, (point.above ?? point.y) - height);
  const left = Math.max(EDGE, Math.min(point.x, view.width - width - EDGE));
  return { top, left };
}
