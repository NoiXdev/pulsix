import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

/** RevisionStep is one write made from this page: it sent If-Match `from`
 * and was answered with revision `to`. Nobody else wrote in between. */
export interface RevisionStep {
  readonly from: number;
  readonly to: number;
}

/**
 * OwnSteps is the group page's record of its own writes to the group: each
 * sent If-Match `from` and was answered with revision `to`, so an edited
 * draft pinned at `from`, or the settings dialog opened on it, may follow
 * it (useRevisionDraft, backlogs 7.89, 7.91). Whoever may follow from a
 * revision holds it, and the record drops the steps that start below both
 * the group's revision and every held one.
 *
 * That is safe: a walk starts at a held revision or at one pinned later,
 * which is at least the group's revision when it is pinned, and the group's
 * revision on the page never decreases, so no walk reaches a dropped step.
 * Between a pin and the effect that holds it, the group's revision bounds
 * the drop. At worst a dropped step turns a follow into a 412 and a stale
 * notice, never into a silent overwrite.
 */
export interface OwnSteps {
  steps: readonly RevisionStep[];
  record(step: RevisionStep): void;
  /** hold keeps the steps from `revision` on until the returned release. */
  hold(revision: number): () => void;
}

/** Outside a group page nothing is recorded, and no draft follows anything. */
export const OwnStepsContext = createContext<OwnSteps>({ steps: [], record: () => undefined, hold: () => () => undefined });

/** useOwnSteps is the record of the group page around the caller. */
export function useOwnSteps(): OwnSteps {
  return useContext(OwnStepsContext);
}

/** followOwnSteps walks the page's own steps from `pinned`, each starting
 * where the last one ended, and only forward. */
export function followOwnSteps(pinned: number, steps: readonly RevisionStep[]): number {
  let revision = pinned;
  for (;;) {
    const step = steps.find((s) => s.from === revision && s.to > revision);
    if (!step) return revision;
    revision = step.to;
  }
}

/** useHeldRevision holds `revision` while it is non-null, holds again when
 * it changes, and releases on unmount. */
export function useHeldRevision(hold: OwnSteps['hold'], revision: number | null): void {
  useEffect(() => (revision === null ? undefined : hold(revision)), [hold, revision]);
}

/** useOwnStepsRecord keeps the record for the group page showing `groupId`
 * at `revision`. It starts over when the page moves to another group, and a
 * write of the group shown before that lands afterwards is not recorded. */
export function useOwnStepsRecord(groupId: string, revision: number): OwnSteps {
  const [record, setRecord] = useState<{ groupId: string; steps: readonly RevisionStep[] }>({ groupId, steps: [] });
  let current = record;
  if (current.groupId !== groupId) {
    current = { groupId, steps: [] };
    setRecord(current);
  }
  // The held revisions, and the group's revision as last rendered.
  const holds = useRef(new Map<symbol, number>());
  const shown = useRef(revision);
  useEffect(() => {
    shown.current = revision;
  }, [revision]);

  const hold = useCallback((held: number) => {
    const key = Symbol();
    holds.current.set(key, held);
    return () => {
      holds.current.delete(key);
    };
  }, []);
  const add = useCallback(
    (step: RevisionStep) => {
      const floor = Math.min(shown.current, ...holds.current.values());
      setRecord((r) => (r.groupId === groupId ? { groupId, steps: [...r.steps.filter((s) => s.from >= floor), step] } : r));
    },
    [groupId],
  );
  const steps = current.steps;
  return useMemo(() => ({ steps, record: add, hold }), [steps, add, hold]);
}
