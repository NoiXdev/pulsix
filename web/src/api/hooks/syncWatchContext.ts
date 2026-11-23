import { createContext, useContext } from 'react';

export type Watch = (org: string, jobId: string) => void;

// Outside a provider (a unit test of one component) watching does nothing.
export const SyncWatchContext = createContext<Watch>(() => undefined);

/** useWatchSync registers a started repository sync's job, so its end is
 * noticed on whatever page is open. */
export function useWatchSync(): Watch {
  return useContext(SyncWatchContext);
}
