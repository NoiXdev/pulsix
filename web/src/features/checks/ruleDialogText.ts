/**
 * The rule dialog's own constant and pure helpers.
 * Split out of RuleDialog.tsx under backlog 3.42, which was past
 * CLAUDE.md's ~400-line signal: none of this renders anything. The two
 * long confirmations that used to live here (a one-way door and a
 * deletion) are catalogue messages now: t.checks.ruleDialog.confirmSwitch
 * and confirmDropClear.
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
