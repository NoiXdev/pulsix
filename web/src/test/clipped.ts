/** Class names that give an element a fixed height or max-height: a spacing
 * step (`h-40`), an arbitrary value (`max-h-[300px]`), `h-px`, the viewport
 * heights (`h-screen`, `max-h-dvh`, ...) and `max-h-full`. */
const FIXED_HEIGHT = /^(max-)?h-(\d|\[|px$|screen$|[sdl]vh$)|^max-h-full$/;

/** isClipped: a class list that hides vertical overflow (`overflow-hidden`,
 * `overflow-clip` or their `-y-` forms) at a fixed height or max-height, which
 * cuts a card's content off. Responsive and state variants (`md:`, `hover:`)
 * are stripped before matching. Only classes: `clippedElements` adds inline
 * styles. */
export function isClipped(classes: string[]): boolean {
  const base = classes.map((c) => c.replace(/^.*:/, ''));
  const hides = base.some((c) => /^overflow(-y)?-(hidden|clip)$/.test(c));
  return hides && base.some((c) => FIXED_HEIGHT.test(c));
}

/** The same reading for an inline `style`: hidden overflow with a height or
 * max-height set. */
function isClippedByStyle(el: HTMLElement): boolean {
  const hides = ['hidden', 'clip'].includes(el.style.overflow) || ['hidden', 'clip'].includes(el.style.overflowY);
  return hides && (el.style.height !== '' || el.style.maxHeight !== '');
}

/** The elements under `root` that hide their overflow at a fixed height, by
 * class or by inline style. */
export function clippedElements(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('*')].filter((el) => isClipped([...el.classList]) || isClippedByStyle(el));
}
