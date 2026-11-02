/**
 * Everything that lets someone choose the GUI's language: the account
 * page's Language card, the setup wizard's language field and the
 * instance administrators' default-language page. The language names
 * themselves are not here (types.ts LOCALE_NAMES): a picker lists each
 * language in its own name.
 */
export const enLanguage = {
  label: 'Language',
  instanceDefault: (language: string) => `Instance default (${language})`,
  instanceDefaultUnknown: 'Instance default',
  account: {
    hint: 'The language of this interface for your account, on every browser and device.',
    saveFailed: 'failed to save the language',
  },
  wizard: {
    hint: 'The default for everyone on this installation. Each person can choose their own later.',
  },
  instance: {
    tab: 'Language',
    title: 'Default language',
    hint: 'Instance-wide: used on the sign-in pages and for everyone who has not chosen a language of their own.',
    field: 'Default language',
    save: 'Save default language',
    saved: 'Default language saved',
    saveFailed: 'failed to save the default language',
  },
};
