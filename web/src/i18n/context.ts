import { createContext } from 'react';
import { en } from './en/index.ts';
import { de } from './de/index.ts';
import type { Locale, Messages } from './types.ts';

export const CATALOGUES: Record<Locale, Messages> = { en, de };

export interface LocaleControl {
  /** The language the GUI is in right now. */
  locale: Locale;
  /**
   * Saves the signed-in user's own choice (null: follow the instance
   * default) and switches the whole tree once the server has it. Rejects
   * with the API's error, so a caller can show it.
   */
  setLocale(locale: Locale | null): Promise<void>;
  /**
   * Switches the language without saving anything — for the setup wizard,
   * which runs before there is a user or an instance default to save it
   * to. Honoured only until setup is complete.
   */
  previewLocale(locale: Locale): void;
}

export interface I18nValue extends LocaleControl {
  messages: Messages;
}

/**
 * The value seen with no provider above: English, saving nothing and
 * fetching nothing. Page and component tests render without a provider,
 * so this is what keeps their English assertions true; a test that wants
 * German wraps its tree in `<I18nProvider locale="de">`.
 */
export const I18nContext = createContext<I18nValue>({
  locale: 'en',
  messages: en,
  setLocale: async () => {},
  previewLocale: () => {},
});
