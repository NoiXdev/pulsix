import type { Template } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

/** What a template's scope says, as text: the whole organization, a group,
 * an explicit host selection, or both halves at once. */
export function scopeText(tpl: Template, groupName: (id: string) => string, t: Messages): string {
  const count = (tpl.scope.host_ids ?? []).length;
  if (!tpl.scope.group_id && count === 0) return t.templates.scope.whole;
  const group = tpl.scope.group_id ? groupName(tpl.scope.group_id) : null;
  if (!group) return t.templates.scope.hosts(count);
  if (count === 0) return group;
  return t.templates.scope.groupAndHosts(group, count);
}
