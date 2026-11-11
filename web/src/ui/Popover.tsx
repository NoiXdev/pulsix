import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react';
import { placePopover, type Placement } from './popoverPlacement.ts';

export type PopoverCloseReason = 'escape' | 'outside';

export interface PopoverProps {
  /** The field the popover belongs to: it is placed against this element,
   *  and a press on it, or on a <label> of it, does not count as outside. */
  anchor: RefObject<HTMLElement | null>;
  onClose: (reason: PopoverCloseReason) => void;
  id?: string;
  /** The popover's accessible name (it is a non-modal dialog). */
  label: string;
  children: ReactNode;
}

/**
 * Popover is the floating panel under a picker's field.
 *
 * It is rendered in place — not portalled to <body> — and positioned
 * `fixed` against the anchor's viewport rectangle. In place matters inside
 * a modal <dialog>: the dialog sits in the browser's top layer and makes
 * everything outside it inert, so a popover portalled to <body> would be
 * drawn underneath the dialog and could not take focus. Fixed matters for
 * the dialog's own `overflow: auto`: a fixed box is not clipped by a
 * scrolling ancestor, so a calendar opened near the bottom of a long form
 * shows whole (Review Focus 2). It follows the field on any scroll or
 * window resize while open, and re-places itself when its own content
 * changes size (a calendar month with six weeks instead of five).
 *
 * Escape closes the popover and nothing else: the handler calls
 * preventDefault(), which is what suppresses a native dialog's close
 * request (see Select.tsx and src/test/setup.ts for why stopPropagation()
 * alone would not).
 */
export function Popover({ anchor, onClose, id, label, children }: PopoverProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState<Placement | null>(null);

  useLayoutEffect(() => {
    const reposition = () => {
      const a = anchor.current;
      const p = ref.current;
      if (!a || !p) return;
      setPlace(placePopover(a.getBoundingClientRect(), p.getBoundingClientRect(), {
        width: window.innerWidth,
        height: window.innerHeight,
      }));
    };
    reposition();
    window.addEventListener('resize', reposition);
    // Capture, so a scroll inside any ancestor (a dialog's body) is seen too.
    window.addEventListener('scroll', reposition, true);
    // A taller month can push it past the viewport's bottom: measure again.
    // Guarded, as an environment without ResizeObserver still gets a popover.
    const resized = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(reposition);
    if (ref.current) resized?.observe(ref.current);
    return () => {
      window.removeEventListener('resize', reposition);
      window.removeEventListener('scroll', reposition, true);
      resized?.disconnect();
    };
  }, [anchor]);

  useEffect(() => {
    // A <label> of the field (its htmlFor, or one wrapping it) stands for
    // the field. A press on it would otherwise close the popover on
    // mousedown and open it again through the click the label passes on
    // to the field — a flicker. So the press keeps focus where it is, and
    // the click is not passed on: the popover simply stays open.
    const onAnchorLabel = (target: EventTarget | null) => {
      const label = target instanceof Element ? target.closest('label') : null;
      return label !== null && anchor.current !== null && label.control === anchor.current;
    };
    const onPointerDown = (e: MouseEvent) => {
      const target = e.target as Node | null;
      if (!target) return;
      if (ref.current?.contains(target) || anchor.current?.contains(target)) return;
      if (onAnchorLabel(target)) {
        e.preventDefault();
        return;
      }
      onClose('outside');
    };
    const onClick = (e: MouseEvent) => {
      if (onAnchorLabel(e.target)) e.preventDefault();
    };
    document.addEventListener('mousedown', onPointerDown);
    // Capture, so the label's click is cancelled before it reaches the field.
    document.addEventListener('click', onClick, true);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('click', onClick, true);
    };
  }, [anchor, onClose]);

  return (
    <div
      ref={ref}
      id={id}
      role="dialog"
      aria-label={label}
      onKeyDown={(e) => {
        if (e.key !== 'Escape') return;
        e.preventDefault();
        e.stopPropagation();
        onClose('escape');
      }}
      // Focus leaving for somewhere else on the page (Tab past the last
      // button) closes it, the way leaving a native picker does. A null
      // relatedTarget is a click on nothing focusable, and focus landing on
      // an ancestor (a click on a focusable dialog's own body) is a click
      // that may well be inside it: both are left to the mousedown
      // listener above, which judges by where the press was.
      onBlur={(e) => {
        const next = e.relatedTarget as Node | null;
        if (!next || e.currentTarget.contains(next) || next.contains(e.currentTarget)) return;
        if (!anchor.current?.contains(next)) onClose('outside');
      }}
      // Never hidden while unplaced, not even for its first render: the
      // calendar focuses its day from an effect that runs before the
      // placement update lands, and a browser ignores focus() inside a
      // `visibility: hidden` box — the arrow keys would then go nowhere.
      // There is nothing to hide anyway: the layout effect above places it
      // before the browser paints.
      style={{ top: place?.top ?? 0, left: place?.left ?? 0 }}
      className="fixed z-50 rounded-md border border-border bg-card p-2 text-foreground shadow-lg"
    >
      {children}
    </div>
  );
}
