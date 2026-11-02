import { useT } from '../../i18n/useT.ts';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';

const TEXTAREA = 'min-h-16 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

const NUMERIC_OPS = ['>', '>=', '<', '<=', '==', '!='];
const DISCRETE_OPS = ['==', '!=', 'in'];

export interface RuleFormProps {
  /** The selected metric's kind (GET /rules/metrics), deciding whether
   * this renders a numeric threshold or a discrete label comparison —
   * the controller's own split: "a discrete metric offers the label
   * operators; a numeric metric offers the comparison operators and the
   * optional clear value." */
  kind: 'numeric' | 'discrete';
  op: string;
  onOpChange: (op: string) => void;
  /** Numeric metrics only; ignored (but still held) for a discrete one. */
  value: string;
  onValueChange: (value: string) => void;
  /** Discrete metrics only: one label for "==" / "!=", one per line for
   * "in". */
  labels: string;
  onLabelsChange: (labels: string) => void;
  /** Numeric metrics only: "" is "no clear condition" — internal/rules
   * Form.Compile refuses a clear condition on a discrete metric outright
   * ("a label does not straddle a threshold"), so this block never
   * renders for one. */
  clearOp: string;
  onClearOpChange: (op: string) => void;
  clearValue: string;
  onClearValueChange: (value: string) => void;
}

/**
 * RuleForm is the threshold editor for a form-mode rule: the operator and
 * its value (or, for a discrete metric, the label it compares against),
 * plus an optional clear condition for hysteresis. The rule's name, scope,
 * metric, severity and duration are common to both editor modes and live
 * in RuleDialog, which also enforces the seeded-rule contract (metric,
 * scope and severity fixed) — none of that applies to the fields here: a
 * seeded rule's threshold, clear condition and duration are exactly what
 * an operator may retune (v0.3b-2 spec, "the seeded-rule contract").
 */
export function RuleForm({
  kind, op, onOpChange, value, onValueChange, labels, onLabelsChange, clearOp, onClearOpChange, clearValue, onClearValueChange,
}: RuleFormProps) {
  const t = useT();
  const ops = kind === 'discrete' ? DISCRETE_OPS : NUMERIC_OPS;
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor="rule-op">{t.checks.ruleDialog.form.operator}</Label>
          <Select id="rule-op" value={op} onChange={onOpChange} options={ops.map((o) => ({ value: o, label: o }))} />
        </div>
        {kind === 'numeric' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-value">{t.checks.ruleDialog.form.value}</Label>
            <Input id="rule-value" type="number" step="any" required value={value} onChange={(e) => onValueChange(e.target.value)} />
          </div>
        )}
        {kind === 'discrete' && op === 'in' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-labels">{t.checks.ruleDialog.form.labels}</Label>
            <textarea id="rule-labels" className={TEXTAREA} required value={labels} onChange={(e) => onLabelsChange(e.target.value)} />
          </div>
        )}
        {kind === 'discrete' && op !== 'in' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-label">{t.checks.ruleDialog.form.label}</Label>
            <Input id="rule-label" required value={labels} onChange={(e) => onLabelsChange(e.target.value)} />
          </div>
        )}
      </div>
      {kind === 'numeric' && (
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-clear-op">{t.checks.ruleDialog.form.clearCondition}</Label>
            <Select
              id="rule-clear-op"
              placeholder={t.checks.ruleDialog.form.noClearCondition}
              value={clearOp}
              onChange={onClearOpChange}
              options={NUMERIC_OPS.map((o) => ({ value: o, label: o }))}
            />
          </div>
          {clearOp !== '' && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="rule-clear-value">{t.checks.ruleDialog.form.clearValue}</Label>
              <Input
                id="rule-clear-value"
                type="number"
                step="any"
                required
                value={clearValue}
                onChange={(e) => onClearValueChange(e.target.value)}
              />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
