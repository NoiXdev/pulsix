/**
 * The rule dialog's own constant, pure helpers and confirmation copy.
 * Split out of RuleDialog.tsx under backlog 3.42, which was past
 * CLAUDE.md's ~400-line signal: none of this renders anything, and the
 * two confirmations are long on purpose — they explain a one-way door and
 * a deletion — so they crowded out the component they belong to.
 */
export const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';

/** lines turns a textarea's content into the string list the API takes,
 * dropping blank lines so a trailing newline is not a label of its own —
 * the same helper CheckDialog's units/mounts fields use. */
export function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** defaultOpFor picks the operator a freshly chosen metric starts with:
 * the first (and most common) comparison for its kind. */
export function defaultOpFor(kind: 'numeric' | 'discrete'): string {
  return kind === 'discrete' ? '==' : '>';
}

export const CONFIRM_SWITCH =
  'Switching to the expression editor is a one-way door from here. The way back to the form editor only stays open while the ' +
  "expression keeps exactly the shape the form produces — one comparison against this metric, no arithmetic, no time window, no \"or\". " +
  'Once it leaves that shape, this rule can only be edited as an expression from now on. Continue?';

/** confirmDropClear is the second, separate confirmation a rule with a
 * clear condition has to pass before it is converted.
 *
 * The expression grammar has no syntax for a clear condition at all, and
 * the server stores clear_ast = NULL for every expression-mode rule, so the
 * conversion does not merely close the way back — it deletes the
 * hysteresis. A rule triggering at >= 85 and clearing at < 80 would start
 * flipping between ok and warn on every crossing of 85, which is the exact
 * behaviour the clear condition was written to prevent, with nothing on
 * screen recording that it happened and no way to recover it by reopening
 * the dialog.
 *
 * It names the condition rather than mentioning one in the abstract, and it
 * is asked every time rather than once per dialog (unlike CONFIRM_SWITCH):
 * this one is destructive, and the operator may well have added the clear
 * condition after the first switch. */
export function confirmDropClear(op: string, value: string): string {
  return (
    `This rule clears at "${op} ${value}". The expression editor has no syntax for a clear condition, so saving as an ` +
    'expression will remove it — the rule will then change state on every crossing of its threshold instead of holding ' +
    'until the clear condition is met. Remove the clear condition and continue?'
  );
}
