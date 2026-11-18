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
  markSaved: (next?: T) => void;
}

/**
 * useCardForm holds the draft of one settings card against the saved value
 * it was loaded from. `dirty` is structural (JSON-safe values). When `saved`
 * changes content — a refetch after a save — a clean draft follows it, a
 * dirty one is left alone. It also registers the unsaved-changes guard.
 */
export function useCardForm<T>(saved: T): CardForm<T> {
  const [base, setBase] = useState<T>(saved);
  const [draft, setDraftState] = useState<T>(saved);
  const [seen, setSeen] = useState<T>(saved);

  // Adjust state during render (React's "derived state" pattern) so the
  // draft never lags a frame behind a refetched value.
  if (seen !== saved) {
    setSeen(saved);
    if (!same(seen, saved) && same(draft, base)) {
      setBase(saved);
      setDraftState(saved);
    }
  }

  const setDraft = useCallback((next: SetStateAction<T>) => {
    setDraftState((cur) => (typeof next === 'function' ? (next as (c: T) => T)(cur) : next));
  }, []);
  const discard = useCallback(() => setDraftState(base), [base]);
  const markSaved = useCallback(
    (next?: T) => {
      const value = next === undefined ? draft : next;
      setBase(value);
      setDraftState(value);
    },
    [draft],
  );

  const dirty = !same(draft, base);
  useUnsavedGuard(dirty);
  return { draft, setDraft, dirty, discard, markSaved };
}
