import { Pill, Tag } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { ItemDecision } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { Format } from '../../i18n/format.ts';
import { DecisionTarget } from './DecisionCells.tsx';

export interface DecisionColumnsContext {
  org: string;
  t: Messages;
  fmt: Pick<Format, 'date'>;
}

/**
 * decisionColumns is every column the item decisions list offers (list id
 * `item-decisions`), sorted in the browser. The pattern is the name column:
 * a decision opens nothing (there is no decision page), so it is plain text
 * with its match kind beside it, and the target keeps its link. The check
 * is a column: the unsorted order keeps one check's decisions together
 * (orderByCheck), the sections the page used to draw.
 */
export function decisionColumns({ org, t, fmt }: DecisionColumnsContext): DataListColumn<ItemDecision>[] {
  const c = t.checks.items.columns;
  return [
    {
      id: 'pattern',
      label: c.pattern,
      width: 'minmax(0, 1.3fr)',
      sortKey: 'pattern',
      sortValue: (r) => r.pattern,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="block truncate font-mono" title={r.pattern}>
          {r.pattern} <span className="font-sans text-muted-foreground">({t.checks.items.matchKind[r.match_kind]})</span>
        </span>
      ),
    },
    {
      id: 'target',
      label: c.target,
      width: 'minmax(0, 1fr)',
      sortKey: 'target',
      sortValue: (r) => (r.scope === 'host' && r.host_id ? r.host_id.slice(0, 8) : t.checks.organization),
      defaultVisible: true,
      render: (r) => <DecisionTarget org={org} decision={r} />,
    },
    {
      id: 'check',
      label: c.check,
      width: '110px',
      sortKey: 'check',
      sortValue: (r) => r.check_key,
      defaultVisible: true,
      render: (r) => <Tag>{r.check_key}</Tag>,
    },
    {
      id: 'verdict',
      label: c.verdict,
      width: '90px',
      sortKey: 'verdict',
      sortValue: (r) => r.decision,
      defaultVisible: true,
      // unknown/ok, not warn/ok: warn is reserved for a degraded monitoring
      // state (ui/badge.tsx's own doc comment), and a verdict is not one.
      render: (r) => <Pill tone={r.decision === 'watch' ? 'ok' : 'unknown'}>{t.checks.items.verdict[r.decision]}</Pill>,
    },
    {
      id: 'note',
      label: c.note,
      width: 'minmax(0, 1.5fr)',
      sortKey: 'note',
      sortValue: (r) => r.note,
      defaultVisible: true,
      render: (r) => (
        <span className={`block truncate text-muted-foreground ${CELL_MINOR}`} title={r.note}>
          {r.note || '—'}
        </span>
      ),
    },
    {
      id: 'created',
      label: c.created,
      width: '110px',
      sortKey: 'created',
      sortValue: (r) => r.created_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(r.created_at)}</span>,
    },
  ];
}
