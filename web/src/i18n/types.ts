import type { en } from './en/index.ts';

/** A language the GUI speaks. The API accepts exactly these two and
 * refuses anything else with 422. */
export type Locale = 'en' | 'de';

export const LOCALES: readonly Locale[] = ['en', 'de'];

/**
 * Messages is the shape of the whole catalogue, taken from the English
 * source (en/index.ts). Every German module is declared against a slice of
 * it (`const deX: Messages['x'] = { … }`), so a key missing from German,
 * an extra one, or one of the wrong kind (a string where English has a
 * message function) fails `tsc -b`.
 */
export type Messages = typeof en;

export function isLocale(value: unknown): value is Locale {
  return value === 'en' || value === 'de';
}

/** Each language's name in that language — the way a language picker
 * lists them, whatever language the page around it is in. Not part of the
 * catalogue because it does not change with the locale. */
export const LOCALE_NAMES: Record<Locale, string> = {
  en: 'English',
  de: 'Deutsch',
};
