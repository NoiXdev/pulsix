import { useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client.ts';
import { useValidateRule } from '../../api/hooks/useRules.ts';
import { Label } from '../../ui/label.tsx';
import type { RuleInput } from '../../api/types.ts';

/** How long the expression field stays quiet before it is checked against
 * the server — the controller's own number (v0.3b-2 spec decisions): a
 * request per keystroke would hammer the parser for no reason, and 400ms
 * is comfortably above ordinary typing cadence without feeling laggy. */
const VALIDATE_DEBOUNCE_MS = 400;

const TEXTAREA = 'min-h-32 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

/** A failed validate names the byte offset in words, "... (at byte N)"
 * (internal/api/rule_handlers.go's mapRulesErr) — the only place that
 * position exists on the wire, since a 422 carries a human message, not a
 * structured field (see useValidateRule's own doc comment). */
const POS_SUFFIX = /\(at byte (\d+)\)$/;

function extractPos(message: string): number | null {
  const m = POS_SUFFIX.exec(message);
  return m ? Number(m[1]) : null;
}

export interface RuleExpressionProps {
  org: string;
  value: string;
  onChange: (value: string) => void;
  /** Every field POST /rules/validate needs besides mode/form/expression:
   * the server type-checks the whole rule, not the expression text in
   * isolation, so a validate call needs the name, scope, metric and
   * severity the rest of the dialog already holds. */
  base: Omit<RuleInput, 'mode' | 'form' | 'expression'>;
  /** Fires whenever the debounced validate settles: `ok` gates the
   * dialog's Save button, `formReady` is the "switching to expression
   * mode is one-way" warning's own live signal (RuleDialog only offers a
   * way back to the form editor while this stays true). */
  onValidChange: (ok: boolean, formReady: boolean) => void;
}

/**
 * RuleExpression is the raw-text editor for an expression-mode rule: a
 * textarea validated against POST /rules/validate on a debounce, never on
 * every keystroke, with the server's own message shown under the field and
 * the caret moved to the byte offset it names (when the field still has
 * focus — a background validate settling must never yank focus away from
 * whatever the operator has since clicked into).
 */
export function RuleExpression({ org, value, onChange, base, onValidChange }: RuleExpressionProps) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), VALIDATE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [value]);

  // Nothing worth asking about yet: an empty expression or a name still
  // blank would answer 422 for a reason that has nothing to do with the
  // expression text (buildRule validates the name before it ever compiles
  // anything — internal/rules/service.go), which would show a confusing
  // error under this field for a problem that lives elsewhere in the form.
  const trimmed = debounced.trim();
  const body: RuleInput | null =
    trimmed === '' || base.name.trim() === '' ? null : { ...base, mode: 'expression', expression: debounced };
  const validate = useValidateRule(org, body);

  const ok = validate.isSuccess;
  const formReady = validate.data?.form_ready ?? false;
  useEffect(() => {
    onValidChange(ok, formReady);
  }, [ok, formReady, onValidChange]);

  const message = validate.isError && validate.error instanceof ApiError ? validate.error.message : null;
  const pos = message ? extractPos(message) : null;

  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (pos === null || !el || document.activeElement !== el) return;
    el.setSelectionRange(pos, pos);
  }, [pos]);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="rule-expression">Expression</Label>
      <textarea
        id="rule-expression"
        ref={ref}
        className={TEXTAREA}
        required
        value={value}
        placeholder="cpu.used_pct >= 90"
        onChange={(e) => onChange(e.target.value)}
      />
      {message && (
        <p role="alert" className="text-xs text-[var(--px-crit)]">
          {message}
        </p>
      )}
    </div>
  );
}
