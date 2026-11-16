import type { Template } from '../../api/types.ts';

/** followUpIds is a template's follow-up ids in start order. */
export function followUpIds(tpl: Pick<Template, 'follow_ups'>): string[] {
  return [...(tpl.follow_ups ?? [])].sort((a, b) => a.position - b.position).map((f) => f.template_id);
}

/** sameIds compares two id lists order-sensitively: the order is the start
 * order of the follow-ups. */
export function sameIds(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}
