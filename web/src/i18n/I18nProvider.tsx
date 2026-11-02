import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useLocaleSources, useSaveMyLocale } from '../api/hooks/useLocaleSettings.ts';
import { CATALOGUES, I18nContext, type I18nValue } from './context.ts';
import { readStoredLocale, resolveLocale, storeLocale } from './resolve.ts';
import type { Locale } from './types.ts';

/**
 * I18nProvider gives its tree the active catalogue (useT), the language
 * and its setters (useLocale) and the formatters (useFormat).
 *
 * Without `locale` it decides the language from the API — the signed-in
 * user's effective language, else the instance default, else, before
 * setup, the browser's (resolve.ts) — and needs a QueryClientProvider
 * above it. main.tsx mounts it this way, above the router.
 *
 * With `locale` the language is fixed and nothing is fetched: for tests
 * that render a tree in German (`<I18nProvider locale="de">`).
 */
export function I18nProvider({ locale, children }: { locale?: Locale; children: ReactNode }) {
  return locale ? (
    <FixedI18n locale={locale}>{children}</FixedI18n>
  ) : (
    <ResolvingI18n>{children}</ResolvingI18n>
  );
}

function FixedI18n({ locale: initial, children }: { locale: Locale; children: ReactNode }) {
  const [locale, setActive] = useState(initial);
  useDocumentLang(locale);
  const value = useMemo<I18nValue>(
    () => ({
      locale,
      messages: CATALOGUES[locale],
      // Nothing is saved: null goes back to the language given.
      setLocale: async (l) => setActive(l ?? initial),
      previewLocale: setActive,
    }),
    [locale, initial],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

function ResolvingI18n({ children }: { children: ReactNode }) {
  const { me, status } = useLocaleSources();
  const { mutateAsync: saveMine } = useSaveMyLocale();
  const [preview, setPreview] = useState<Locale | null>(null);
  const resolved = resolveLocale({ me, status, preview, browser: navigator.language });

  // The first render must not wait for the network: until a source has
  // answered — and again for the moment between a session reset and the
  // next answer — the last language known is used, starting from the one
  // remembered from the previous visit. Adjusting state during render is
  // React's own pattern for state derived from changing input.
  const [lastKnown, setLastKnown] = useState<Locale>(() => readStoredLocale() ?? 'en');
  if (resolved && resolved !== lastKnown) setLastKnown(resolved);
  const locale = resolved ?? lastKnown;

  useEffect(() => storeLocale(locale), [locale]);
  useDocumentLang(locale);

  const value = useMemo<I18nValue>(
    () => ({
      locale,
      messages: CATALOGUES[locale],
      setLocale: async (l) => {
        await saveMine(l);
      },
      previewLocale: setPreview,
    }),
    [locale, saveMine],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

/** Keeps `<html lang>` on the active language, for screen readers,
 * hyphenation and the browser's own translation offer. */
function useDocumentLang(locale: Locale) {
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
}
