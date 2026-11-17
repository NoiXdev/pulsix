import { Pill, Tag } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { MonitoringGroup, Rule } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { RuleTarget } from './RuleCells.tsx';
import { ruleTargetText } from './ruleTarget.ts';
import { intervalLabel } from './interval.ts';

export interface RuleColumnsContext {
  org: string;
  t: Messages;
  groups: MonitoringGroup[];
}

/**
 * ruleColumns is every column the rules list offers (list id `rules`),
 * sorted in the browser. A rule opens nothing (there is no rule page), so
 * the name is plain text and the target keeps its link. The metric the rule
 * watches is a column: the unsorted order keeps one metric's rules together
 * (orderByMetric), the sections the page used to draw.
 */
export function ruleColumns({ org, t, groups }: RuleColumnsContext): DataListColumn<Rule>[] {
  const c = t.checks.rules.columns;
  return [
    {
      id: 'rule',
      label: c.rule,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 items-center gap-2">
          <span className="block truncate text-foreground">{r.name}</span>
          <Tag>{t.checks.rules.origin[r.origin]}</Tag>
        </span>
      ),
    },
    {
      id: 'metric',
      label: c.metric,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'metric',
      sortValue: (r) => r.metric,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.metric}</span>,
    },
    {
      id: 'severity',
      label: c.severity,
      width: '90px',
      sortKey: 'severity',
      sortValue: (r) => r.severity,
      defaultVisible: true,
      render: (r) => <Pill tone={r.severity === 'crit' ? 'crit' : 'warn'}>{t.checks.severity[r.severity]}</Pill>,
    },
    {
      id: 'applies_to',
      label: c.appliesTo,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'applies_to',
      sortValue: (r) => ruleTargetText(r, groups, t.checks.organization, t.checks.groupFallback),
      defaultVisible: true,
      render: (r) => <RuleTarget org={org} rule={r} groups={groups} />,
    },
    {
      id: 'for',
      label: c.for,
      width: '90px',
      sortKey: 'for',
      sortValue: (r) => r.for_seconds,
      defaultVisible: true,
      render: (r) => <span>{intervalLabel(r.for_seconds, t)}</span>,
    },
    {
      id: 'state',
      label: c.state,
      width: '100px',
      sortKey: 'state',
      sortValue: (r) => r.enabled,
      defaultVisible: true,
      render: (r) => (r.enabled ? <Pill tone="ok">{t.checks.enabled}</Pill> : <Pill tone="unknown">{t.checks.disabled}</Pill>),
    },
  ];
}
