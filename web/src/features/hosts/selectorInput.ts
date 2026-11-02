/**
 * selectorInput is the small parser behind the tag-chip editor on the host
 * detail page's tags editor (a single `key=value` chip at a time, not the
 * full boolean selector grammar the query box on the hosts list uses —
 * see internal/hosts/selector's own doc comment for that grammar).
 *
 * A host tag's key must be lowercase (internal/hosts.tagKeyPattern, which
 * this mirrors on the client so a bad key is rejected inline before a
 * round trip to PUT …/hosts/{id}/tags ever happens): letters, digits,
 * underscore, dot and hyphen, starting with a letter or digit.
 */
import type { Messages } from '../../i18n/types.ts';

const TAG_KEY_PATTERN = /^[a-z0-9][a-z0-9_.-]*$/;

export interface ParsedTag {
  key: string;
  value: string;
}

export type ParseTagResult = { ok: true; tag: ParsedTag } | { ok: false; error: string };

/** parseTagChip turns raw chip input like "role=db" into {key, value},
 * rejecting anything that is not exactly one `key=value` pair with a
 * lowercase key. */
export function parseTagChip(input: string, t: Messages): ParseTagResult {
  const trimmed = input.trim();
  if (trimmed === '') {
    return { ok: false, error: t.hosts.tag.enter };
  }

  const eq = trimmed.indexOf('=');
  if (eq === -1) {
    return { ok: false, error: t.hosts.tag.format };
  }

  const key = trimmed.slice(0, eq).trim();
  const value = trimmed.slice(eq + 1).trim();

  if (key === '') {
    return { ok: false, error: t.hosts.tag.format };
  }
  if (!TAG_KEY_PATTERN.test(key)) {
    return {
      ok: false,
      error: t.hosts.tag.invalidKey(key),
    };
  }

  return { ok: true, tag: { key, value } };
}
