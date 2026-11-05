/** isClipped: a class list that hides vertical overflow (`overflow-hidden`,
 * `overflow-clip` or their `-y-` forms) at a fixed height or max-height, which
 * cuts a card's content off. Responsive and state variants (`md:`, `hover:`)
 * are stripped before matching. */
export function isClipped(classes: string[]): boolean {
  const base = classes.map((c) => c.replace(/^.*:/, ''));
  const hides = base.some((c) => /^overflow(-y)?-(hidden|clip)$/.test(c));
  const fixedHeight = /^(max-)?h-(\d|\[)/;
  return hides && base.some((c) => fixedHeight.test(c));
}

/** The elements under `root` that hide their overflow at a fixed height. */
export function clippedElements(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('*')].filter((el) => isClipped([...el.classList]));
}
