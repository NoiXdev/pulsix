import type { Tag } from '../../api/types.ts';
import type { ParsedTag } from './selectorInput.ts';

/**
 * manualTagPairs returns the manual tags as `{key, value}` pairs, the shape
 * `PUT /hosts/{id}/tags` takes. An automatic (fact) tag is left out: the
 * server stores every tag it is sent as manual, so echoing a fact tag back
 * would freeze it against later fact changes.
 */
export function manualTagPairs(tags: readonly Tag[] | null | undefined): ParsedTag[] {
  return (tags ?? []).filter((tag) => tag.source === 'manual').map((tag) => ({ key: tag.key, value: tag.value }));
}
