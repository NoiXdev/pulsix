import { isLocale, type Locale } from './types.ts';

/** What the provider knows when it decides the language. Each source is
 * whatever the API has answered so far, or undefined while it has not. */
export interface LocaleSources {
  /** GET /auth/me — absent when nobody is signed in. */
  me?: { effective_locale?: string | null };
  /** /auth/me has not answered yet but is on its way. A 401 settles it,
   * so this is false for a page without a session once that is known. */
  meLoading?: boolean;
  /** GET /setup/status — public. */
  status?: { completed?: boolean; default_locale?: string | null };
  /** The setup wizard's unsaved choice (LocaleControl.previewLocale). */
  preview: Locale | null;
  /** navigator.language. */
  browser?: string;
}

/**
 * resolveLocale applies the spec's order: the signed-in user's effective
 * language (their own choice, else the instance default — the server
 * resolves that), else the instance default for pages without a session,
 * else — before setup, when there is no default yet — the wizard's own
 * choice or the browser's language when it is German. null means no
 * source has answered yet; the provider then keeps the last known one.
 * While /auth/me is still loading the instance default is not used: it
 * usually answers first, and a user whose own language differs would see
 * the instance's until /auth/me lands (and lose the remembered one).
 */
export function resolveLocale(s: LocaleSources): Locale | null {
  const own = s.me?.effective_locale;
  if (isLocale(own)) return own;
  if (s.status?.completed === false) return s.preview ?? browserLocale(s.browser);
  if (s.meLoading) return null;
  const instance = s.status?.default_locale;
  return isLocale(instance) ? instance : null;
}

export function browserLocale(language: string | undefined): Locale {
  return language?.toLowerCase().startsWith('de') ? 'de' : 'en';
}

const STORAGE_KEY = 'pulsix.locale';

/** The language of the previous visit, so a reload renders in it straight
 * away instead of flashing English until the API answers. Storage can be
 * unavailable (private windows, blocked site data); that is not an error. */
export function readStoredLocale(): Locale | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return isLocale(v) ? v : null;
  } catch {
    return null;
  }
}

export function storeLocale(locale: Locale): void {
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    // Remembering is a convenience; the API stays the source of truth.
  }
}
