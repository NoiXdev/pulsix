import { useSyncExternalStore } from 'react';
import { hashKey, useMutation, useQuery, useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { GuiLocale } from '../types.ts';
import { meQueryKey, useMe } from './useMe.ts';
import { setupStatusQueryKey, useSetupStatus } from './useSetupStatus.ts';

const instanceLocaleQueryKey = ['admin', 'locale'] as const;

/**
 * Re-renders the caller whenever the query under `key` is removed from or
 * added back to the cache. A query observer stays attached to the query it
 * was built with: after QueryClient.clear() (lib/session.ts, at every
 * sign-in and sign-out) it keeps reading the dead one until its component
 * renders again. Pages are remounted by the navigation that follows a
 * session change; the i18n provider above the router is not, so without
 * this it would keep the previous session's language.
 */
function useFollowCacheResets(qc: QueryClient, key: QueryKey) {
  const cache = qc.getQueryCache();
  const hash = hashKey(key);
  useSyncExternalStore(
    (onChange) => cache.subscribe(onChange),
    () => cache.get(hash),
  );
}

/**
 * useLocaleSources is what the i18n provider decides the language from:
 * the signed-in principal (GET /auth/me, which carries `effective_locale`)
 * and the public setup status (`completed`, `default_locale`). Either is
 * undefined until it answers; /auth/me stays undefined without a session.
 */
export function useLocaleSources() {
  const qc = useQueryClient();
  const me = useMe();
  const status = useSetupStatus();
  useFollowCacheResets(qc, meQueryKey);
  useFollowCacheResets(qc, setupStatusQueryKey);
  return { me: me.data, status: status.data };
}

/**
 * PUT /auth/me/locale — the signed-in user's own language, or null to
 * follow the instance default. The answer carries the resolved language,
 * written straight into the cached /auth/me so every reader — the i18n
 * provider first — switches without another round trip.
 */
export function useSaveMyLocale() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (locale: GuiLocale | null) => unwrap(api.PUT('/api/v1/auth/me/locale', { body: { locale } })),
    onSuccess: (res) => {
      qc.setQueryData(meQueryKey, (old) =>
        old ? { ...old, locale: res.locale, effective_locale: res.effective_locale } : old,
      );
    },
  });
}

/** The instance's default language (instance administrators only). */
export function useInstanceLocale() {
  return useQuery({
    queryKey: instanceLocaleQueryKey,
    queryFn: () => unwrap(api.GET('/api/v1/admin/settings/locale')),
  });
}

/**
 * Replaces the instance default. The API answers 204, so everything that
 * carries the default is fetched again: this setting, the public setup
 * status, and /auth/me, whose `effective_locale` is the default for a user
 * with no language of their own.
 */
export function useSaveInstanceLocale() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (defaultLocale: GuiLocale) =>
      unwrap(api.PUT('/api/v1/admin/settings/locale', { body: { default_locale: defaultLocale } })),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: instanceLocaleQueryKey }),
        qc.invalidateQueries({ queryKey: setupStatusQueryKey }),
        qc.invalidateQueries({ queryKey: meQueryKey }),
      ]),
  });
}
