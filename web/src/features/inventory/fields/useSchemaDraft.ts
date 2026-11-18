import { useState } from 'react';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useReloadInventory, useSetGroupSchema } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { problemsFromError } from '../../variables/paths.ts';
import type { Problem, VarSchema } from '../../variables/types.ts';
import { refusedWith, shownProblems, type Reported } from '../sections/draftProblems.ts';
import { useRevisionDraft } from '../sections/useRevisionDraft.ts';
import { useOwnSteps } from '../ownSteps.ts';
import { useT } from '../../../i18n/useT.ts';

export interface SchemaDraft {
  draft: VarSchema;
  edit: (next: VarSchema) => void;
  /** Edited since it was last built from the stored schema. */
  touched: boolean;
  /** Bumps when the draft is replaced wholesale: the editor's key. */
  generation: number;
  /** The problems a save was refused with, beside the draft they describe. */
  problems: readonly Problem[];
  /** A save of this draft was refused as stale (412). */
  stale: boolean;
  saving: boolean;
  /** Saves the draft on its revision (If-Match). Resolves true when the
   * draft now is what was stored (nothing typed while the save was in
   * flight), false when it was refused or an edit is still unsaved. */
  save: () => Promise<boolean>;
  reload: () => Promise<void>;
  discard: () => void;
}

/**
 * useSchemaDraft is the group schema's draft and its save, as the old
 * SchemaSection held them. The draft and the revision its save sends
 * follow useRevisionDraft (backlog 7.89): an unedited draft follows the
 * group, an edited one saves on the revision it was edited on (or one the
 * page's own writes moved it to). A save's stale notice and the problems it
 * was refused with show only beside the generation it was started on,
 * whatever revision the draft has followed since, until a later save of
 * that draft succeeds (backlog 7.91).
 */
export function useSchemaDraft(org: string, group: InventoryGroup): SchemaDraft {
  const t = useT();
  const toast = useToast();
  const mutation = useSetGroupSchema(org, group.id);
  const own = useOwnSteps();
  const d = useRevisionDraft(group.schema, group.revision, toVarSchema, own);
  // What a save reported for the draft it was started on.
  const [reported, setReported] = useState<Reported | null>(null);
  // The generation a save was refused as stale on; a later save that
  // succeeds clears it.
  const [staleOn, setStaleOn] = useState<number | null>(null);
  const reloadGroup = useReloadInventory(org);

  // A reload that brings other content starts the draft over; one that
  // does not keeps it, which then saves on the reloaded revision.
  async function reload() {
    const fresh = await reloadGroup(group.id);
    d.reloaded(fresh.schema, fresh.revision);
    setStaleOn(null);
  }

  async function save(): Promise<boolean> {
    const ticket = d.startSave();
    setReported({ on: ticket, problems: [], answer: false });
    try {
      const res = await mutation.mutateAsync({ schema: d.draft, revision: d.revision });
      own.record({ from: d.revision, to: res.group.revision, kind: 'content' });
      const rebuilt = d.saved(ticket, res.group.schema, res.group.revision);
      setStaleOn(null);
      toast.show(res.incomplete_members > 0 ? t.inventory.schemaTab.savedIncomplete(res.incomplete_members) : t.inventory.schemaTab.saved);
      return rebuilt;
    } catch (err) {
      if (errorStatus(err) === 412) {
        setStaleOn(ticket.generation);
        return false;
      }
      const ps = problemsFromError(err, 'body.schema');
      if (ps.length > 0) setReported(refusedWith(ticket, ps));
      else toast.show(err instanceof Error ? err.message : t.inventory.schemaTab.saveFailed, 'destructive');
      return false;
    }
  }

  return {
    draft: d.draft,
    edit: d.edit,
    touched: d.touched,
    generation: d.generation,
    problems: shownProblems(reported, { generation: d.generation, revision: d.revision }, []),
    stale: staleOn === d.generation,
    saving: mutation.isPending,
    save,
    reload,
    discard() {
      d.discard();
      setReported(null);
    },
  };
}
