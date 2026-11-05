import { Link } from '@tanstack/react-router';
import { Pill, Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import type { ItemDecision } from '../../api/types.ts';

/** DecisionRow is one item decision. It opens nothing (there is no decision
 * page), so its cells keep the target link. A writer's ⋯ menu offers Edit and
 * Delete…; `columns` reserves the menu's 40px as its last track, left empty
 * here. */
export function DecisionRow({
  org, decision: d, columns, canWrite, onEdit, onDelete,
}: {
  org: string;
  decision: ItemDecision;
  columns: string;
  canWrite: boolean;
  onEdit: (d: ItemDecision) => void;
  onDelete: (d: ItemDecision) => void;
}) {
  const t = useT();
  const fmt = useFormat();
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.checks.items.decisionActions(d.pattern)}>
            <RowMenuItem onSelect={() => onEdit(d)}>{t.checks.edit}</RowMenuItem>
            <RowMenuItem onSelect={() => onDelete(d)} destructive>
              {t.checks.deleteItem}
            </RowMenuItem>
          </RowMenu>
        )
      }
    >
      <DecisionTarget org={org} decision={d} />
      <span>
        <Tag>{d.check_key}</Tag>
      </span>
      <span className="truncate font-mono text-[13px]" title={d.pattern} data-row>
        {d.pattern} <span className="text-muted-foreground">({t.checks.items.matchKind[d.match_kind]})</span>
      </span>
      <span>
        {/* unknown/ok, not warn/ok: warn is reserved for a degraded
            monitoring state (ui/badge.tsx's own doc comment), and a
            verdict is not one — the same reasoning a rule's own
            enabled/disabled Pill already follows for its state. */}
        <Pill tone={d.decision === 'watch' ? 'ok' : 'unknown'}>{t.checks.items.verdict[d.decision]}</Pill>
      </span>
      <span className="truncate text-[13px] text-muted-foreground" title={d.note}>
        {d.note || '—'}
      </span>
      <span className="text-[13px] text-muted-foreground">{fmt.date(d.created_at)}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}

/** DecisionTarget renders a decision's scope as what it applies to — the
 * organization, or a host — mirroring RuleRow's own RuleTarget.
 * ItemDecisionDTO carries no host_name (unlike RuleDTO's backlog-3.30
 * enrichment), so a host-scoped decision shows its shortened id, exactly
 * the fallback RuleTarget itself used before that enrichment existed. */
function DecisionTarget({ org, decision }: { org: string; decision: ItemDecision }) {
  const t = useT();
  if (decision.scope === 'host' && decision.host_id) {
    return (
      <Link
        to="/o/$org/hosts/$id"
        params={{ org, id: decision.host_id }}
        className="truncate font-mono text-[13px] text-foreground hover:text-primary"
      >
        {decision.host_id.slice(0, 8)}
      </Link>
    );
  }
  return <span className="truncate text-[13px] text-muted-foreground">{t.checks.organization}</span>;
}
