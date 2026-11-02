import { enAccount } from './account.ts';
import { enActivity } from './activity.ts';
import { enAgents } from './agents.ts';
import { enAlerts } from './alerts.ts';
import { enAuth } from './auth.ts';
import { enCalendar } from './calendar.ts';
import { enChecks } from './checks.ts';
import { enCommon } from './common.ts';
import { enFleet } from './fleet.ts';
import { enHosts } from './hosts.ts';
import { enInstance } from './instance.ts';
import { enLanguage } from './language.ts';
import { enMaintenance } from './maintenance.ts';
import { enMembers } from './members.ts';
import { enNotifications } from './notifications.ts';
import { enRoutes } from './routes.ts';
import { enSchedules } from './schedules.ts';
import { enSettings } from './settings.ts';
import { enSetup } from './setup.ts';
import { enShell } from './shell.ts';
import { enTokens } from './tokens.ts';
import { enTriggers } from './triggers.ts';
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
  alerts: enAlerts,
  auth: enAuth,
  calendar: enCalendar,
  checks: enChecks,
  common: enCommon,
  fleet: enFleet,
  hosts: enHosts,
  instance: enInstance,
  language: enLanguage,
  maintenance: enMaintenance,
  members: enMembers,
  notifications: enNotifications,
  routes: enRoutes,
  schedules: enSchedules,
  settings: enSettings,
  setup: enSetup,
  shell: enShell,
  tokens: enTokens,
  triggers: enTriggers,
  ui: enUi,
  validation: enValidation,
  workers: enWorkers,
};
