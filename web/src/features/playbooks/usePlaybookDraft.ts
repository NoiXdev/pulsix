import { useCallback, useState } from 'react';

interface State {
  /** The stored text and revision last seen from the server. */
  seen: string;
  seenRevision: number;
  /** The stored text the draft is compared with: what was loaded or adopted,
   * then what this page's last save stored. */
  saved: string;
  /** The revision `saved` came from: what a save sends as If-Match. */
  revision: number;
  draft: string;
  /** `seen` arrived after `saved` while the draft was dirty: it is the newer
   * stored text, and the one to adopt once the draft is clean again. Not
   * set by this page's own save, whose `saved` is newer than `seen` until
   * the stored text catches up. */
  behind: boolean;
}

const adopt = (stored: string, revision: number): State => ({ seen: stored, seenRevision: revision, saved: stored, revision, draft: stored, behind: false });

/**
 * usePlaybookDraft holds the playbook page's draft of an editor playbook's
 * text and the revision its base came from (backlog 3.127). While the draft
 * is clean (equal to `saved`) it follows the server: a newer stored text or
 * revision, written elsewhere, replaces both, so the page never shows stale
 * text (the group page's useRevisionDraft does the same). A dirty draft
 * keeps the person's text and its base revision, so its save is refused
 * (412) when someone saved meanwhile; the newer stored text only becomes
 * the one last seen, and is adopted when the person reverts the draft to
 * `saved` by hand or asks for it (`reload`). A newer revision of the very
 * text the draft started from moves the base along: nothing to overwrite.
 * `markSaved` records what this page's own save stored and the revision the
 * server answered.
 */
export function usePlaybookDraft(stored: string, storedRevision: number) {
  const [state, setState] = useState<State>(() => adopt(stored, storedRevision));
  let current = state;
  if (stored !== state.seen || storedRevision !== state.seenRevision) {
    if (state.draft === state.saved) current = adopt(stored, storedRevision);
    else if (stored === state.saved) current = { ...state, seen: stored, seenRevision: storedRevision, revision: storedRevision, behind: false };
    else current = { ...state, seen: stored, seenRevision: storedRevision, behind: true };
  } else if (state.behind && state.draft === state.saved) {
    current = adopt(state.seen, state.seenRevision);
  }
  if (current !== state) setState(current);
  const setDraft = useCallback((draft: string) => setState((s) => ({ ...s, draft })), []);
  const markSaved = useCallback((text: string, revision: number) => setState((s) => ({ ...s, saved: text, revision, behind: false })), []);
  const reload = useCallback(() => setState((s) => adopt(s.seen, s.seenRevision)), []);
  return { saved: current.saved, draft: current.draft, revision: current.revision, setDraft, markSaved, reload };
}
