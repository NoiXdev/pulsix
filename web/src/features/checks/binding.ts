import type { Messages } from '../../i18n/types.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';

/* Kept apart from CheckRow.tsx: a module that exports components may export
 * nothing else (react-refresh/only-export-components). */

/** bindingText names what a definition is bound to as the thing itself —
 * the organization, a named monitoring group, a named host — never as the
 * raw id the API carries (only its short form, when the name is unknown).
 * host_name comes from the definition itself (internal/api's checkHostNames,
 * one read for the whole page). This used to be a useHost call per row,
 * which on an unpaginated GET /checks meant one request per host-bound
 * definition on every mount — and host binding IS the per-host override, so
 * that count tracks the fleet size (review finding 2). */
export function bindingText(check: CheckDefinition, groups: MonitoringGroup[], t: Messages): string {
  if (check.binding_kind === 'host' && check.host_id) return check.host_name ?? check.host_id.slice(0, 8);
  if (check.binding_kind === 'group' && check.group_id) {
    const group = groups.find((g) => g.id === check.group_id);
    return group?.name ?? t.checks.groupFallback(check.group_id.slice(0, 8));
  }
  return t.checks.organization;
}
