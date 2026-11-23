import type { OutlineEntry } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

type OutlineMessages = Messages['playbooks']['page']['outline'];

/** The name an outline entry shows: its own, or — the server sends "" when
 * the entry has none it can use — a word for its kind in the reader's
 * language. */
export function outlineEntryName(entry: Pick<OutlineEntry, 'name' | 'kind'>, o: OutlineMessages): string {
  if (entry.name) return entry.name;
  return Object.hasOwn(o.unnamedKind, entry.kind) ? o.unnamedKind[entry.kind as keyof OutlineMessages['unnamedKind']] : o.unnamed;
}
