import type { Messages } from '../types.ts';
import { deAccount } from './account.ts';
import { deActivity } from './activity.ts';
import { deAgents } from './agents.ts';
import { deAlerts } from './alerts.ts';
import { deAuth } from './auth.ts';
import { deBackups } from './backups.ts';
import { deCalendar } from './calendar.ts';
import { deChecks } from './checks.ts';
import { deCommon } from './common.ts';
import { deCredentials } from './credentials.ts';
import { deFleet } from './fleet.ts';
import { deHosts } from './hosts.ts';
import { deInstance } from './instance.ts';
import { deInventory } from './inventory.ts';
import { deLanguage } from './language.ts';
import { deMaintenance } from './maintenance.ts';
import { deMembers } from './members.ts';
import { deNotifications } from './notifications.ts';
import { dePlaybooks } from './playbooks.ts';
import { deRepos } from './repos.ts';
import { deRoutes } from './routes.ts';
import { deRuns } from './runs.ts';
import { deSchedules } from './schedules.ts';
import { deSettings } from './settings.ts';
import { deSetup } from './setup.ts';
import { deShell } from './shell.ts';
import { deTemplates } from './templates.ts';
import { deTokens } from './tokens.ts';
import { deTriggers } from './triggers.ts';
import { deUsers } from './users.ts';
import { deUi } from './ui.ts';
import { deValidation } from './validation.ts';
import { deVariables } from './variables.ts';
import { deWorkers } from './workers.ts';

/**
 * The German catalogue, typed as Messages: a key missing here, an extra
 * one, or a string where English has a message function fails `tsc -b`.
 * catalogue.test.ts additionally refuses an empty German string.
 *
 * German style (spec "German style") — fixed once, here:
 *
 *   Run                → Lauf
 *   Template           → Vorlage
 *   Inventory          → Inventar
 *   Credential         → Zugangsdaten
 *   Jump host          → Sprunghost
 *   Schedule           → Zeitplan
 *   Channel            → Kanal
 *   Alert              → Alarm
 *   Maintenance window → Wartungsfenster
 *   Settings           → Einstellungen
 *
 *   Unchanged: Playbook, Host, Worker, Agent, Check, Job, Token, Zone.
 *
 * Neutral imperatives and nouns, no "du" and, where avoidable, no "Sie":
 * "Speichern", "Host hinzufügen", "Passwort ändern". Validation messages
 * stay lower case, like their English originals.
 */
export const de: Messages = {
  account: deAccount,
  activity: deActivity,
  agents: deAgents,
  alerts: deAlerts,
  auth: deAuth,
  backups: deBackups,
  calendar: deCalendar,
  checks: deChecks,
  common: deCommon,
  credentials: deCredentials,
  fleet: deFleet,
  hosts: deHosts,
  instance: deInstance,
  inventory: deInventory,
  language: deLanguage,
  maintenance: deMaintenance,
  members: deMembers,
  notifications: deNotifications,
  playbooks: dePlaybooks,
  repos: deRepos,
  routes: deRoutes,
  runs: deRuns,
  schedules: deSchedules,
  settings: deSettings,
  setup: deSetup,
  shell: deShell,
  templates: deTemplates,
  tokens: deTokens,
  triggers: deTriggers,
  users: deUsers,
  ui: deUi,
  validation: deValidation,
  variables: deVariables,
  workers: deWorkers,
};
