import { SlidersHorizontal } from 'lucide-react';
import { AssertionTable } from '../../ui/AssertionTable.tsx';
import { Button } from '../../ui/button.tsx';
import { StackedBar } from '../../ui/StackedBar.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import type { EffectiveRule, HostCheck, MonitoringGroup } from '../../api/types.ts';
import { decisionOriginLabel, offersForget, type TupleKey } from './hostChecks.ts';
import { EffectiveRuleEntries } from './EffectiveRulesPanel.tsx';
import { ForgetItemButton } from './ForgetItemButton.tsx';
import { ItemDecisionButton } from './ItemDecisionButton.tsx';
import { assertionRows, assertionValue, timingParts } from './httpCheck.ts';

export interface HostCheckHttpProps {
  org: string;
  hostId: string;
  /** `checks.write` — gates Ignore/Watch and Forget; the Rules toggle and
   * the ignored marker are `checks.read` facts. */
  canWrite: boolean;
  frozen?: boolean;
  check: HostCheck;
  effective: EffectiveRule[];
  groups: MonitoringGroup[];
  openRules: TupleKey | null;
  onOpenRules: (key: TupleKey | null) => void;
}

/** The metric whose rules and decision an assertion row stands for. */
const ASSERTION_METRIC = 'http.assertion';

/**
 * HostCheckHttp is an http check's own section on the host page (backlog
 * 3.13b), below its values: the latest attempt's timing breakdown as one
 * stacked bar (the phases it reported, with their times) and its
 * assertions as a table. Either is left out when there is nothing to show.
 * Backlog 3.89: the assertion items are not listed as generic value rows,
 * so their actions live in the table — Ignore/Watch and Forget (item-wide)
 * for a writer, the Rules toggle, and the ignored marker.
 */
export function HostCheckHttp({ org, hostId, canWrite, frozen = false, check, effective, groups, openRules, onOpenRules }: HostCheckHttpProps) {
  const t = useT();
  const format = useFormat();
  const parts = timingParts(check, t);
  const base = assertionRows(check, t, format, new Date());
  if (parts.length === 0 && base.length === 0) return null;

  const rows = base.map((row) => {
    const name = row.name;
    const result = assertionValue(check, ASSERTION_METRIC, name);
    const first = (check.values ?? []).find((v) => v.item === name);
    const rules = effective.filter((e) => e.check_key === check.key && (e.item ?? '') === name && e.metric === ASSERTION_METRIC);
    const isRulesOpen = openRules !== null && openRules.check === check.key && openRules.item === name && openRules.metric === ASSERTION_METRIC;
    const decidedBy = result?.decided_by;
    const ignored = result?.ignored ?? false;
    const forgettable = !frozen && first !== undefined && offersForget(check, first, canWrite);
    const actions = [
      result !== undefined && canWrite && (
        <ItemDecisionButton key="decision" org={org} hostId={hostId} checkKey={check.key} item={name} ignored={ignored} decidedBy={decidedBy} />
      ),
      forgettable && <ForgetItemButton key="forget" org={org} hostId={hostId} checkKey={check.key} item={name} />,
      rules.length > 0 && (
        <Button
          key="rules"
          type="button"
          variant="ghost"
          aria-label={t.hosts.value.rulesFor(check.key, `${name} ${ASSERTION_METRIC}`)}
          aria-expanded={isRulesOpen}
          onClick={() => onOpenRules(isRulesOpen ? null : { check: check.key, item: name, metric: ASSERTION_METRIC })}
        >
          <SlidersHorizontal size={14} /> {t.hosts.value.rules}
        </Button>
      ),
    ].filter(Boolean);
    const marker = (decidedBy || ignored) && (
      <details className="text-[11px] text-muted-foreground">
        <summary className="cursor-pointer">
          {decidedBy ? (decidedBy.decision === 'ignore' ? t.hosts.value.whyIgnored : t.hosts.value.whyWatched) : t.hosts.value.whyIgnored}
        </summary>
        <p className="mt-1">
          {decidedBy ? (
            <>
              {decisionOriginLabel(decidedBy, t)}
              {decidedBy.note && ` — ${decidedBy.note}`}
            </>
          ) : (
            t.hosts.value.originUnknown
          )}
        </p>
      </details>
    );
    const panel = isRulesOpen && (
      <div className="rounded-md border border-border p-2">
        <EffectiveRuleEntries entries={rules} groups={groups} showTuple={false} />
      </div>
    );
    return {
      ...row,
      actions: actions.length > 0 ? <span className="flex items-center gap-1">{actions}</span> : undefined,
      detail: marker || panel ? <div className="flex flex-col gap-1">{marker}{panel}</div> : undefined,
    };
  });

  return (
    <div className="mt-2 flex flex-col gap-2 rounded-md border border-border p-2">
      {parts.length > 0 && <StackedBar label={t.hosts.http.timingTitle(check.key)} parts={parts} />}
      {rows.length > 0 && <AssertionTable label={t.hosts.http.assertionsTitle(check.key)} columns={t.hosts.http.columns} rows={rows} />}
    </div>
  );
}
