import { createContext, useCallback, useContext, useMemo, useState } from 'react';
import type { RevisionStep } from './sections/useRevisionDraft.ts';

/** OwnSteps is the group page's record of its own writes to the group: each
 * sent If-Match `from` and was answered with revision `to`, so an edited
 * draft pinned at `from` may follow it (useRevisionDraft, backlog 7.89). */
export interface OwnSteps {
  steps: readonly RevisionStep[];
  record(step: RevisionStep): void;
}

/** Outside a group page nothing is recorded, and no draft follows anything. */
export const OwnStepsContext = createContext<OwnSteps>({ steps: [], record: () => undefined });

/** useOwnSteps is the record of the group page around the caller. */
export function useOwnSteps(): OwnSteps {
  return useContext(OwnStepsContext);
}

/** useOwnStepsRecord keeps the record for the group page showing `groupId`.
 * It starts over when the page moves to another group, and a write of the
 * group shown before that lands afterwards is not recorded. */
export function useOwnStepsRecord(groupId: string): OwnSteps {
  const [record, setRecord] = useState<{ groupId: string; steps: readonly RevisionStep[] }>({ groupId, steps: [] });
  let current = record;
  if (current.groupId !== groupId) {
    current = { groupId, steps: [] };
    setRecord(current);
  }
  const add = useCallback(
    (step: RevisionStep) => setRecord((r) => (r.groupId === groupId ? { groupId, steps: [...r.steps, step] } : r)),
    [groupId],
  );
  const steps = current.steps;
  return useMemo(() => ({ steps, record: add }), [steps, add]);
}
