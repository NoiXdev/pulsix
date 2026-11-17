/** The version this page load started with. Module state on purpose: it must
 * survive the watcher unmounting (the lock screen replaces the page) yet
 * start over with a reload, which a sessionStorage entry would not. */
export const baseline: { value: string | null } = { value: null };

/** For tests. */
export function resetVersionBaseline() {
  baseline.value = null;
}
