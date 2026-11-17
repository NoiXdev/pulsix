/** The version of the first successful /setup/status read of this page load,
 * whichever reader made it, so it is the version the loaded SPA was served
 * with, not whatever a later watcher happens to see. Recorded by the query
 * function in api/hooks/useSetupStatus.ts.
 *
 * Module state on purpose: it must
 * survive the watcher unmounting (the lock screen replaces the page) yet
 * start over with a reload, which a sessionStorage entry would not. */
export const versionBaseline: { value: string | null } = { value: null };

/** For tests. */
export function resetVersionBaseline() {
  versionBaseline.value = null;
}
