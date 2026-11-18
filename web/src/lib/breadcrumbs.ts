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
 * host page reads "Hosts > web-04.fra" rather than a piece of its id.
 */
export function crumbLabels(pathname: string, orgSlug: string, t: Messages, names: Readonly<Record<string, string>> = {}): string[] {
  const prefix = `/o/${orgSlug}/`;
  if (!pathname.startsWith(prefix)) return [];
  const segments = pathname.slice(prefix.length).split('/').filter(Boolean);
  const labels: Record<string, string> = t.shell.crumbs;
  const idLabels: Record<string, string> = t.shell.crumbIds;
  // Own keys only: a host id that happens to read "constructor" is not a label.
  return segments.map((segment, i) => {
    if (Object.hasOwn(labels, segment)) return labels[segment];
    const parent = segments[i - 1];
    const named = parent === undefined ? undefined : `${parent}/${segment}`;
    if (named !== undefined && Object.hasOwn(names, named)) return names[named];
    // A person's id under members/ or users/ is named by what it is: a
    // shortened id says nothing about a person.
    if (parent !== undefined && Object.hasOwn(idLabels, parent)) return idLabels[parent];
    return shortenId(segment);
  });
}

/** hostIdInPath is the host id of a host page (`/o/<org>/hosts/<id>`),
 * undefined for any other path. */
export function hostIdInPath(pathname: string, orgSlug: string): string | undefined {
  const prefix = `/o/${orgSlug}/hosts/`;
  if (!pathname.startsWith(prefix)) return undefined;
  const rest = pathname.slice(prefix.length).split('/').filter(Boolean);
  return rest.length === 1 ? rest[0] : undefined;
}
