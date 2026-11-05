import { useRef, useState } from 'react';

/** SaveTicket is what a save remembers when it starts: the edits counted
 * so far, so its answer can tell whether one was typed while it was in
 * flight. */
export interface SaveTicket {
  readonly edits: number;
}

/** RevisionStep is one write made from this page: it sent If-Match `from`
 * and was answered with revision `to`. Nobody else wrote in between. */
export interface RevisionStep {
  readonly from: number;
  readonly to: number;
}

export interface RevisionDraft<D> {
  /** What the form shows. */
  draft: D;
  /** What a save sends as If-Match. */
  revision: number;
  /** Bumps whenever the draft is replaced wholesale (editor key, problem
   * reset). */
  generation: number;
  edit(next: D): void;
  startSave(): SaveTicket;
  saved(ticket: SaveTicket, stored: unknown, revision: number): void;
  reloaded(stored: unknown, revision: number): void;
  discard(): void;
}

interface State<D> {
  draft: D;
  /** The content (JSON) the draft was built from. */
  base: string;
  /** The stored content (JSON) the last render was given. */
  seen: string;
  touched: boolean;
  pinned: number;
  generation: number;
}

const contentKey = (stored: unknown) => JSON.stringify(stored ?? null);

/** follow walks the page's own steps from `pinned`, each starting where the
 * last one ended. */
function follow(pinned: number, steps: readonly RevisionStep[]): number {
  let revision = pinned;
  for (;;) {
    const step = steps.find((s) => s.from === revision && s.to > revision);
    if (!step) return revision;
    revision = step.to;
  }
}

/**
 * useRevisionDraft holds a form's draft of a group's stored content and the
 * revision a save of it sends as its If-Match (backlogs 7.86, 7.89).
 *
 * While the draft is untouched (never edited) it follows the group: other
 * stored content rebuilds it (`generation` bumps), and the revision follows
 * group.revision — the draft then equals the stored content at that
 * revision, so saving on the newest revision overwrites nothing. It is the
 * newer of the group's revision and the one this form's last save stored,
 * should the refetch after that save not have landed (or failed); for the
 * same reason the content a save stored is not replaced by the older
 * content the group still shows, only by content that changes after it.
 *
 * The first edit pins the revision it was made on, and a background
 * refetch that brings a newer revision — with the same content or other —
 * no longer moves it: saving on that revision would overwrite a change the
 * user never saw. An edit typed back to the start still counts as one. The
 * one exception is the page's own writes (`ownSteps`): a write from this
 * page that sent If-Match r and got r' back means nobody else wrote in
 * between, so a draft pinned at r follows it to r' (and on along a chain of
 * such steps). A step that does not start at the pinned revision is not
 * followed, so someone else's write in between still ends in a 412.
 *
 * A successful save pins the revision it stored and makes what it stored
 * the draft's base; with no edit typed while it was in flight the draft is
 * rebuilt from it, untouched again (a rebuilt draft never holds a typed
 * secret, which would otherwise ride along on a later save past someone
 * else's rotation); an edit typed meanwhile is kept, still edited.
 *
 * A Reload pins the reloaded revision. Content other than the base starts
 * the draft over; the same content keeps it, edited if it was, since the
 * kept draft still is not the stored content. Discard rebuilds the draft
 * from the stored content and follows the group again.
 */
export function useRevisionDraft<D>(
  stored: unknown,
  revision: number,
  build: (stored: unknown) => D,
  ownSteps: readonly RevisionStep[],
): RevisionDraft<D> {
  const key = contentKey(stored);
  const [state, setState] = useState<State<D>>(() => ({
    draft: build(stored),
    base: key,
    seen: key,
    touched: false,
    pinned: revision,
    generation: 0,
  }));
  // Counts edits, so a save can tell whether one was typed while it was in
  // flight.
  const edits = useRef(0);

  // New stored content: an untouched draft is rebuilt from it, unless it is
  // what the draft already holds (the content its own save stored).
  let s = state;
  if (key !== s.seen) {
    s = !s.touched && key !== s.base ? { ...s, seen: key, draft: build(stored), base: key, generation: s.generation + 1 } : { ...s, seen: key };
    setState(s);
  }

  const current = (st: State<D>) => (st.touched ? follow(st.pinned, ownSteps) : Math.max(st.pinned, revision));

  return {
    draft: s.draft,
    revision: current(s),
    generation: s.generation,
    edit(next) {
      edits.current += 1;
      setState((st) => (st.touched ? { ...st, draft: next } : { ...st, draft: next, touched: true, pinned: current(st) }));
    },
    startSave() {
      return { edits: edits.current };
    },
    saved(ticket, savedContent, savedRevision) {
      const untouched = edits.current === ticket.edits;
      const savedKey = contentKey(savedContent);
      setState((st) =>
        untouched
          ? { ...st, draft: build(savedContent), base: savedKey, touched: false, pinned: savedRevision, generation: st.generation + 1 }
          : { ...st, base: savedKey, pinned: savedRevision },
      );
    },
    reloaded(fresh, freshRevision) {
      const freshKey = contentKey(fresh);
      setState((st) =>
        freshKey === st.base
          ? { ...st, pinned: freshRevision }
          : { ...st, draft: build(fresh), base: freshKey, touched: false, pinned: freshRevision, generation: st.generation + 1 },
      );
    },
    discard() {
      setState((st) => ({ ...st, draft: build(stored), base: key, touched: false, pinned: revision, generation: st.generation + 1 }));
    },
  };
}
