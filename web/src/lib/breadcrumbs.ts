import type { Messages } from '../i18n/types.ts';

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
 * shown verbatim, shortened if it looks like a UUID; the id after members/
 * or users/ reads "Member" or "Account" instead. The labels come from
 * the catalogue (`t.shell.crumbs`), so the trail follows the language.
 *
 * `names` gives an id segment a name the caller already knows, keyed by
 * the segment before it and the id (`hosts/<id>` → the host's name), so a
 * host page reads "Hosts > web-04.fra" rather than a piece of its id. A
 * list of names stands for one crumb each: a group page reads "Groups >
 * prod > web > web_eu", its ancestors before its own name.
 */
export function crumbLabels(
  pathname: string,
  orgSlug: string,
  t: Messages,
  names: Readonly<Record<string, string | readonly string[]>> = {},
): string[] {
  const prefix = `/o/${orgSlug}/`;
  if (!pathname.startsWith(prefix)) return [];
  const segments = pathname.slice(prefix.length).split('/').filter(Boolean);
  const labels: Record<string, string> = t.shell.crumbs;
  const idLabels: Record<string, string> = t.shell.crumbIds;
  // Own keys only: a host id that happens to read "constructor" is not a label.
  return segments.flatMap((segment, i) => {
    if (Object.hasOwn(labels, segment)) return labels[segment];
    const parent = segments[i - 1];
    const named = parent === undefined ? undefined : `${parent}/${segment}`;
    if (named !== undefined && Object.hasOwn(names, named) && names[named].length > 0) return names[named];
    // A person's id under members/ or users/ is named by what it is: a
    // shortened id says nothing about a person.
    if (parent !== undefined && Object.hasOwn(idLabels, parent)) return idLabels[parent];
    return shortenId(segment);
  });
}

/** The id of a detail page directly under `/o/<org>/<section>/`. */
function idInPath(pathname: string, orgSlug: string, section: string): string | undefined {
  const prefix = `/o/${orgSlug}/${section}/`;
  if (!pathname.startsWith(prefix)) return undefined;
  const rest = pathname.slice(prefix.length).split('/').filter(Boolean);
  return rest.length === 1 ? rest[0] : undefined;
}

/** hostIdInPath is the host id of a host page (`/o/<org>/hosts/<id>`),
 * undefined for any other path. */
export function hostIdInPath(pathname: string, orgSlug: string): string | undefined {
  return idInPath(pathname, orgSlug, 'hosts');
}

/** groupIdInPath is the group id of a group page (`/o/<org>/groups/<id>`),
 * undefined for any other path. */
export function groupIdInPath(pathname: string, orgSlug: string): string | undefined {
  return idInPath(pathname, orgSlug, 'groups');
}

/** playbookIdInPath is the playbook id of a playbook page
 * (`/o/<org>/playbooks/<id>`), undefined for any other path and for the new
 * playbook page (`/playbooks/new`). */
export function playbookIdInPath(pathname: string, orgSlug: string): string | undefined {
  const id = idInPath(pathname, orgSlug, 'playbooks');
  return id === 'new' ? undefined : id;
}
