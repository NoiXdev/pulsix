import { enActivity } from './activity.ts';
import { enCommon } from './common.ts';
import { enLanguage } from './language.ts';
import { enRoutes } from './routes.ts';
import { enSettings } from './settings.ts';
import { enShell } from './shell.ts';
import { enTokens } from './tokens.ts';
import { enUi } from './ui.ts';
import { enValidation } from './validation.ts';

/**
 * The English catalogue — the source of truth for every key. It is
 * assembled from one module per feature (en/<feature>.ts exporting
 * `en<Feature>`), registered here under the feature's key and, with the
 * same key, in de/index.ts. Its type is `Messages` (types.ts).
 *
 * A leaf is either a string or a message function for anything with
 * interpolation or a plural:
 *
 *   hostsCount: (n: number) => (n === 1 ? '1 host' : `${n} hosts`),
 */
export const en = {
  activity: enActivity,
  common: enCommon,
  language: enLanguage,
  routes: enRoutes,
  settings: enSettings,
  shell: enShell,
  tokens: enTokens,
  ui: enUi,
  validation: enValidation,
};
