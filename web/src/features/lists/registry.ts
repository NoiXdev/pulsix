import type { Messages } from '../../i18n/types.ts';

/**
 * Every list built on DataList, with the ids of the columns it offers.
 * These ids are what the preferences document stores (`lists.<id>.columns`),
 * so they are a contract: renaming one forgets the users' saved choice for
 * it (mergeColumns drops the unknown id), and each must match the server's
 * `^[a-z][a-z0-9_.-]{0,63}$` or every later save is refused (registry.test
 * checks). useListView refuses, in development and tests, a column that is
 * not listed here. A list's name for the settings page is
 * `lists.names.<id>` in both catalogues.
 *
 * A migration task adds its list here with its columns.
 */
const LISTS = {
  hosts: ['name', 'zone', 'tags', 'managed', 'monitored', 'last_seen', 'active_runs', 'agent_version', 'os'],
  runs: ['run', 'kind', 'status', 'trigger', 'template', 'zone', 'created'],
  alerts: ['alert', 'open', 'peak', 'tracked', 'acknowledged', 'mute'],
  users: ['email', 'name', 'state', 'orgs', 'last_sign_in'],
  activity: ['at', 'actor', 'action', 'object'],
  'my-activity': ['at', 'organization', 'action', 'object'],
  repos: ['name', 'url', 'branch', 'subpath', 'credential', 'last_sync'],
  playbooks: ['name', 'source', 'repository', 'updated'],
  templates: ['name', 'playbook', 'scope', 'credential'],
  schedules: ['schedule', 'status', 'template', 'cron', 'next', 'notify', 'last'],
  'run-requests': ['reason', 'status', 'template', 'kind', 'requested'],
  'host-runs': ['run', 'kind', 'status', 'trigger', 'template', 'zone', 'created'],
  groups: ['name', 'members', 'fields', 'values', 'jump_host'],
  'group-members': ['host', 'values'],
  'group-children': ['name', 'members'],
  credentials: ['name', 'kind', 'used_by', 'expiry', 'created'],
  'jump-hosts': ['name', 'login', 'reached', 'credential', 'host_key'],
  agents: ['host', 'agent', 'status', 'last_seen', 'version', 'certificate'],
  'agent-tokens': ['token', 'uses', 'bound', 'expires'],
  workers: ['worker', 'zone', 'status', 'last_seen', 'version', 'certificate', 'current_job'],
  'worker-enrollments': ['enrollment', 'expires'],
} as const satisfies Record<string, readonly string[]>;

export type ListId = keyof typeof LISTS;

export const LIST_COLUMNS: Readonly<Record<ListId, readonly string[]>> = LISTS;

/** A list's name in the page's language; an id this build does not know
 * (a list saved by a newer or older version) is shown as itself. */
export function listName(id: string, t: Messages): string {
  const names: Record<string, string> = t.lists.names;
  return Object.hasOwn(names, id) ? names[id] : id;
}
