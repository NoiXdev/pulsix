import { Link } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { CELL_MINOR } from '../../ui/data-list/index.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import { bindingText } from './binding.ts';

/** Binding renders bindingText, as a link to the host or to the monitoring
 * groups for a definition bound to one. */
export function Binding({ org, check, groups }: { org: string; check: CheckDefinition; groups: MonitoringGroup[] }) {
  const t = useT();
  const text = bindingText(check, groups, t);
  if (check.binding_kind === 'host' && check.host_id) {
    return (
      <Link to="/o/$org/hosts/$id" params={{ org, id: check.host_id }} className="relative z-10 block truncate font-mono hover:text-primary">
        {text}
      </Link>
    );
  }
  if (check.binding_kind === 'group' && check.group_id) {
    return (
      <Link to="/o/$org/checks/monitoring-groups" params={{ org }} search={{}} className="relative z-10 block truncate hover:text-primary">
        {text}
      </Link>
    );
  }
  return <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{text}</span>;
}
