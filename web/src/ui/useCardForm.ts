import { useCallback, useState } from 'react';
import type { SetStateAction } from 'react';
import { useUnsavedGuard } from './useUnsavedGuard.ts';

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

export interface CardForm<T> {
  draft: T;
  setDraft: (next: SetStateAction<T>) => void;
  dirty: boolean;
  discard: () => void;
  /** markSaved records a successful save: `saved` (what the server now
   * holds) becomes the baseline. The draft becomes `saved` only while it
   * still equals `submitted` (what the save sent; `saved` when omitted);
   * an edit typed while the save was in flight is kept and stays dirty
   * against the new baseline. */
  markSaved: (saved: T, submitted?: T) => void;
}

/**
 * useCardForm holds the draft of one settings card against the saved value
 * it was loaded from. `dirty` is structural (JSON-safe values). When `saved`
 * changes content — a refetch after a save — a clean draft follows it, a
 * dirty one is left alone, and Discard brings back that latest saved value
 * rather than the one the draft started from. It also registers the unsaved-changes guard,
 * unless `guard` is false.
 */
export function useCardForm<T>(saved: T, { guard = true }: { guard?: boolean } = {}): CardForm<T> {
  const [base, setBase] = useState<T>(saved);
  const [draft, setDraftState] = useState<T>(saved);
  const [seen, setSeen] = useState<T>(saved);

  // Adjust state during render (React's "derived state" pattern) so the
  // draft never lags a frame behind a refetched value.
  // By content, not identity: callers pass a fresh object every render.
  if (!same(seen, saved)) {
    setSeen(saved);
    if (same(draft, base)) {
      setBase(saved);
      setDraftState(saved);
    }
  }

  const setDraft = useCallback((next: SetStateAction<T>) => {
    setDraftState((cur) => (typeof next === 'function' ? (next as (c: T) => T)(cur) : next));
  }, []);
  const discard = useCallback(() => {
    setBase(seen);
    setDraftState(seen);
  }, [seen]);
  const markSaved = useCallback((value: T, submitted: T = value) => {
    setBase(value);
    setDraftState((cur) => (same(cur, submitted) ? value : cur));
  }, []);

  const dirty = !same(draft, base);
  // A page with several card forms passes guard: false to each and calls
  // useUnsavedGuard once with any of them dirty, so leaving asks once.
  useUnsavedGuard(guard && dirty);
  return { draft, setDraft, dirty, discard, markSaved };
}
