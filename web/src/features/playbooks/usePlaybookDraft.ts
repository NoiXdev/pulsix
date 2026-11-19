import { useCallback, useState } from 'react';

interface State {
  /** The stored text last seen from the server. */
  seen: string;
  /** The stored text the draft is compared with: what was loaded or adopted,
   * then what this page's last save stored. */
  saved: string;
  draft: string;
}

/**
 * usePlaybookDraft holds the playbook page's draft of an editor playbook's
 * text. While the draft is clean (equal to `saved`) it follows the server:
 * a newer stored text, written elsewhere, replaces both, so the page never
 * shows stale text and a later save does not quietly undo someone else's
 * change (the group page's useRevisionDraft does the same with revisions).
 * A dirty draft keeps the person's text; the newer stored text only becomes
 * the one last seen. `markSaved` records what this page's own save stored.
 */
export function usePlaybookDraft(stored: string) {
  const [state, setState] = useState<State>({ seen: stored, saved: stored, draft: stored });
  let current = state;
  if (stored !== state.seen) {
    current = state.draft === state.saved ? { seen: stored, saved: stored, draft: stored } : { ...state, seen: stored };
    setState(current);
  }
  const setDraft = useCallback((draft: string) => setState((s) => ({ ...s, draft })), []);
  const markSaved = useCallback((text: string) => setState((s) => ({ ...s, saved: text })), []);
  return { saved: current.saved, draft: current.draft, setDraft, markSaved };
}
