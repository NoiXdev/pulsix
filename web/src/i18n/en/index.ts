import { enAccount } from './account.ts';
import { enActivity } from './activity.ts';
import { enAgents } from './agents.ts';
import { enAuth } from './auth.ts';
import { enCommon } from './common.ts';
import { enFleet } from './fleet.ts';
import { enHosts } from './hosts.ts';
import { enInstance } from './instance.ts';
import { enLanguage } from './language.ts';
import { enMembers } from './members.ts';
import { enRoutes } from './routes.ts';
import { enSettings } from './settings.ts';
import { enSetup } from './setup.ts';
import { enShell } from './shell.ts';
import { enTokens } from './tokens.ts';
import { enUi } from './ui.ts';
import { enValidation } from './validation.ts';
import { enWorkers } from './workers.ts';

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
  account: enAccount,
  activity: enActivity,
  agents: enAgents,
  auth: enAuth,
  common: enCommon,
  fleet: enFleet,
  hosts: enHosts,
  instance: enInstance,
  language: enLanguage,
  members: enMembers,
  routes: enRoutes,
  settings: enSettings,
  setup: enSetup,
  shell: enShell,
  tokens: enTokens,
  ui: enUi,
  validation: enValidation,
  workers: enWorkers,
};
