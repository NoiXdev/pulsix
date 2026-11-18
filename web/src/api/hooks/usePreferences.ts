import { useCallback } from 'react';
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { Preferences } from '../types.ts';

export const preferencesQueryKey = ['preferences'] as const;

/** What a user who never changed anything has (spec: today's behaviour),
 * and what the hooks answer until the saved document has loaded. */
export const DEFAULT_PREFERENCES: Preferences = { density: 'standard', page_size: 25, lists: {} };

/** A change to the preferences, applied to the latest document. */
export type PreferencesUpdate = (prev: Preferences) => Preferences;

async function fetchPreferences(): Promise<Preferences> {
  return strip(await unwrap(api.GET('/api/v1/auth/me/preferences')));
}

/** Drops the answer's `$schema` link, so the document goes back as it came. */
function strip(doc: Preferences & { $schema?: string }): Preferences {
  return { density: doc.density, page_size: doc.page_size, lists: doc.lists ?? {} };
}

/** How long the cached document counts as fresh for a mount; a window focus
 * always refetches, so another tab's save shows up when this tab is used. */
const STALE_MS = 30_000;

/**
 * The signed-in user's preferences. Refetched when the window gets focus and
 * on a mount once stale, so a tab picks up another tab's saves (backlog
 * 3.104); this tab's own saves keep the cache current. Until the GET
 * has answered — or when it failed — `prefs` holds the defaults and
 * `loaded` is false. `ready` turns true once the GET has answered either
 * way: from then on `prefs` is all there will be.
 */
export function usePreferences(): { prefs: Preferences; loaded: boolean; ready: boolean } {
  const q = useQuery({
    queryKey: preferencesQueryKey,
    queryFn: fetchPreferences,
    staleTime: STALE_MS,
    refetchOnWindowFocus: 'always',
    refetchOnMount: true,
  });
  return { prefs: q.data ?? DEFAULT_PREFERENCES, loaded: q.isSuccess, ready: q.status !== 'pending' };
}

/** One save in flight or queued. `ctx` is set once its change is in the
 * cache: the document before it, for a rollback. Kept on the variables
 * object (which is this save's own) so a failing earlier save can rebase
 * the later ones synchronously. */
interface SaveVars {
  update: PreferencesUpdate;
  ctx?: { previous: Preferences };
}

/** Waits until a change can be applied to the cached document. A loaded
 * one is ready once any refetch is called off, so it cannot overwrite the
 * change. Before the first answer the saved document is fetched (or the
 * running fetch awaited): applying the change to the defaults would send
 * them and wipe what the user saved. A failed fetch fails the save. */
async function ready(qc: QueryClient): Promise<void> {
  if (qc.getQueryState(preferencesQueryKey)?.status === 'success') {
    await qc.cancelQueries({ queryKey: preferencesQueryKey });
  } else {
    await qc.fetchQuery({ queryKey: preferencesQueryKey, queryFn: fetchPreferences, staleTime: STALE_MS });
  }
}

/**
 * Saves a change to the preferences (PUT, the whole document). The change
 * is an updater, applied to the cached document at once, so every reader
 * sees it before the server answers. The PUTs are serialized (one mutation
 * scope) and each sends the cached document as it is when it goes out, so
 * a change made while another is in flight is never lost (ruling P4). A
 * failed PUT takes its own change back out of the cache and keeps the
 * changes made after it. The returned promise rejects with the API's error.
 */
export function useSavePreferences(): (update: PreferencesUpdate) => Promise<Preferences> {
  const qc = useQueryClient();
  const { mutateAsync } = useMutation({
    mutationKey: preferencesQueryKey,
    scope: { id: 'preferences' },
    onMutate: async (vars: SaveVars) => {
      await ready(qc);
      // Read and write with no wait between: another save's change, applied
      // meanwhile, is kept. Gone meanwhile (a lock or sign-out emptied the
      // cache): fail rather than save the defaults with this change.
      const previous = qc.getQueryData<Preferences>(preferencesQueryKey);
      if (!previous) throw new Error('preferences not loaded');
      qc.setQueryData(preferencesQueryKey, vars.update(previous));
      vars.ctx = { previous };
    },
    mutationFn: async () => {
      const body = qc.getQueryData<Preferences>(preferencesQueryKey);
      // Gone from the cache (a lock or sign-out cleared it): nothing to send.
      if (!body) throw new Error('preferences not loaded');
      return strip(await unwrap(api.PUT('/api/v1/auth/me/preferences', { body })));
    },
    onSuccess: (saved) => {
      // The answer is what the server holds; take it unless a later change
      // is still on its way, which the cache already shows.
      if (qc.isMutating({ mutationKey: preferencesQueryKey }) <= 1) qc.setQueryData(preferencesQueryKey, saved);
    },
    onError: (_err, vars) => rollBack(qc, vars),
  });
  return useCallback((update: PreferencesUpdate) => mutateAsync({ update }), [mutateAsync]);
}

/** Takes a failed save's change out of the cache: its document from before,
 * with the changes of the saves after it applied again, in order. */
function rollBack(qc: QueryClient, failed: SaveVars) {
  // Without a query (a lock or sign-out emptied the cache) there is nothing
  // to take back, and setting it would bring the document back.
  if (!failed.ctx || !qc.getQueryState(preferencesQueryKey)) return;
  const pending = qc
    .getMutationCache()
    .findAll({ mutationKey: preferencesQueryKey, status: 'pending' })
    .map((m) => m.state.variables as SaveVars);
  const self = pending.indexOf(failed);
  let doc = failed.ctx.previous;
  for (const later of self < 0 ? [] : pending.slice(self + 1)) {
    if (!later.ctx) continue; // not applied yet: it will read the cache set here
    later.ctx.previous = doc;
    doc = later.update(doc);
  }
  qc.setQueryData(preferencesQueryKey, doc);
}
