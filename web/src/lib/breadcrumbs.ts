const SEGMENT_LABEL: Record<string, string> = {
  hosts: 'Hosts',
  settings: 'Settings',
  members: 'Members',
  activity: 'Activity',
  tokens: 'Tokens',
  mail: 'Outgoing mail',
  groups: 'Groups',
  repos: 'Repositories',
  playbooks: 'Playbooks',
  templates: 'Templates',
  runs: 'Runs',
  credentials: 'Credentials',
  'jump-hosts': 'Jump hosts',
};

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function shortenId(segment: string): string {
  return UUID_RE.test(segment) ? segment.slice(0, 8) : segment;
}

/**
 * crumbLabels derives a breadcrumb trail from the actual URL path under
 * /o/{org}/... instead of a capitalised nav key (finding "Important 6",
 * task-15a-report.md fix round 1): a nav key like 'hosts' can only ever
 * produce one crumb and has no way to express a nested route like
 * Settings > Members. A segment this app has no label for (a host id) is
 * shown verbatim, shortened if it looks like a UUID.
 */
export function crumbLabels(pathname: string, orgSlug: string): string[] {
  const prefix = `/o/${orgSlug}/`;
  if (!pathname.startsWith(prefix)) return [];
  const segments = pathname.slice(prefix.length).split('/').filter(Boolean);
  return segments.map((segment) => SEGMENT_LABEL[segment] ?? shortenId(segment));
}
