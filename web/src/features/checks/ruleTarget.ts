import type { MonitoringGroup, Rule } from '../../api/types.ts';

/** ruleTargetText is a rule's scope as what it applies to — the
 * organization, a named monitoring group, a named host — never as the raw id
 * the API carries. RuleDTO carries host_name and group_name directly
 * (backlog 3.30); a rule whose host or group was deleted falls back to the
 * shortened id. The group list is still consulted as the fallback source for
 * a group name: group_name is absent exactly when the group cannot be read,
 * and the organization's own, separately loaded monitoring-group list may
 * still know it in the narrow window between the two reads. */
export function ruleTargetText(rule: Rule, groups: MonitoringGroup[], organization: string, groupFallback: (id: string) => string): string {
  if (rule.scope === 'group' && rule.group_id) {
    return rule.group_name ?? groups.find((g) => g.id === rule.group_id)?.name ?? groupFallback(rule.group_id.slice(0, 8));
  }
  if ((rule.scope === 'host' || rule.scope === 'item') && rule.host_id) {
    return `${rule.host_name ?? rule.host_id.slice(0, 8)}${rule.scope === 'item' && rule.item ? ` / ${rule.item}${rule.item_check_key ? ` (${rule.item_check_key})` : ''}` : ''}`;
  }
  return organization;
}
