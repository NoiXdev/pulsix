import { useEffect, useState } from 'react';
import { usePlaybookOutlineQuery } from '../../api/hooks/usePlaybooks.ts';
import type { PlaybookOutline } from '../../api/types.ts';

/** How long the text must stay unchanged before its outline is asked for. */
export const OUTLINE_DEBOUNCE_MS = 400;

/** useDebounced follows `value` once it has stayed the same for `ms`; the
 * first value at once. */
function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    if (Object.is(settled, value)) return;
    const timer = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms, settled]);
  return settled;
}

/**
 * usePlaybookOutline is the server's outline of an editor playbook's text
 * (spec §3/§4): asked for once typing pauses (OUTLINE_DEBOUNCE_MS), and
 * while the text does not parse (422) the last good outline stays, with
 * `invalid` set. Off (`enabled` false, a repository playbook) it asks
 * nothing and answers null.
 */
export function usePlaybookOutline(org: string, content: string, enabled: boolean): { outline: PlaybookOutline | null; invalid: boolean } {
  const settled = useDebounced(content, OUTLINE_DEBOUNCE_MS);
  const query = usePlaybookOutlineQuery(org, settled, { enabled });
  const [good, setGood] = useState<PlaybookOutline | null>(null);
  const fresh = query.data && !query.isPlaceholderData ? query.data : null;
  if (fresh && fresh !== good) setGood(fresh);
  if (!enabled) return { outline: null, invalid: false };
  return { outline: query.data ?? good, invalid: query.isError };
}
