import { useContext } from 'react';
import { I18nContext, type LocaleControl } from './context.ts';
import type { Messages } from './types.ts';

/**
 * useT returns the active catalogue: `const t = useT(); … {t.hosts.title}`
 * or `{t.hosts.count(n)}` for a message function. It is safe anywhere,
 * src/ui included — it reads context and fetches nothing.
 */
export function useT(): Messages {
  return useContext(I18nContext).messages;
}

/** useLocale returns the active language and the two ways to change it
 * (see LocaleControl). */
export function useLocale(): LocaleControl {
  return useContext(I18nContext);
}
