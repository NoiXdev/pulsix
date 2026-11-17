import { Link } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { CELL_MINOR } from '../../ui/data-list/index.ts';
import type { MonitoringGroup, Rule } from '../../api/types.ts';
import { ruleTargetText } from './ruleTarget.ts';

/** RuleTarget renders ruleTargetText, as a link to the monitoring groups or
 * to the host for a rule scoped to one. */
export function RuleTarget({ org, rule, groups }: { org: string; rule: Rule; groups: MonitoringGroup[] }) {
  const t = useT();
  const text = ruleTargetText(rule, groups, t.checks.organization, t.checks.groupFallback);
  if (rule.scope === 'group' && rule.group_id) {
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} search={{}} className="relative z-10 block truncate hover:text-primary">
        {text}
      </Link>
    );
  }
  if ((rule.scope === 'host' || rule.scope === 'item') && rule.host_id) {
    return (
      <Link to="/o/$org/hosts/$id" params={{ org, id: rule.host_id }} className="relative z-10 block truncate font-mono hover:text-primary">
        {text}
      </Link>
    );
  }
  return <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{text}</span>;
}
