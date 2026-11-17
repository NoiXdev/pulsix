import { Link } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { CELL_MINOR } from '../../ui/data-list/index.ts';
import type { ItemDecision } from '../../api/types.ts';

/** DecisionTarget renders a decision's scope as what it applies to — the
 * organization, or a host. ItemDecisionDTO carries no host_name (unlike
 * RuleDTO's backlog-3.30 enrichment), so a host-scoped decision shows its
 * shortened id. */
export function DecisionTarget({ org, decision }: { org: string; decision: ItemDecision }) {
  const t = useT();
  if (decision.scope === 'host' && decision.host_id) {
    return (
      <Link to="/o/$org/hosts/$id" params={{ org, id: decision.host_id }} className="relative z-10 block truncate font-mono hover:text-primary">
        {decision.host_id.slice(0, 8)}
      </Link>
    );
  }
  return <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{t.checks.organization}</span>;
}
