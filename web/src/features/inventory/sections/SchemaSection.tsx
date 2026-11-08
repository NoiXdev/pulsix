import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useReloadInventory, useSetGroupSchema } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { VariableEditor } from '../../variables/VariableEditor.tsx';
import { problemsFromError } from '../../variables/paths.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { refusedWith, shownProblems, type Reported } from './draftProblems.ts';
import { useRevisionDraft } from './useRevisionDraft.ts';
import { useOwnSteps } from '../ownSteps.ts';
import { useT } from '../../../i18n/useT.ts';

/** SchemaSection holds the group's variable schema editor (the full-width
 * VariableEditor, group variant: manual fields only). Discard and Save sit
 * in the section header for a writer; a reader gets the editor read-only,
 * so the fields still open, but in a dialog that saves nothing. The draft
 * and the revision its save sends follow useRevisionDraft (backlog 7.89):
 * an unedited draft follows the group, an edited one saves on the revision
 * it was edited on (or one the page's own writes moved it to). A save's
 * problems and stale notice show only beside the draft (generation and
 * revision) it was started on. Any other draft shows neither (backlog
 * 7.91). */
export function SchemaSection({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const save = useSetGroupSchema(org, group.id);
  const own = useOwnSteps();
  // The editor keeps per-field state keyed by field identity (expanded
  // rows, sub-field lists); a wholesale replacement of the draft bumps
  // `generation`, which remounts it, or it could keep showing state from
  // before.
  const { draft, revision, generation, edit, startSave, saved, reloaded, discard } = useRevisionDraft(group.schema, group.revision, toVarSchema, own.steps);
  // What a save reported for the draft it was started on.
  const [reported, setReported] = useState<Reported | null>(null);
  // The generation a save was refused as stale on.
  const [staleOn, setStaleOn] = useState<number | null>(null);
  const problems = shownProblems(reported, { generation, revision }, []);

  const reload = useReloadInventory(org);

  // A reload that brings other content starts the draft over; one that
  // does not keeps it, which then saves on the reloaded revision.
  async function onReload() {
    const fresh = await reload(group.id);
    reloaded(fresh.schema, fresh.revision);
    setStaleOn(null);
  }

  async function onSave() {
    const ticket = startSave();
    setReported({ on: ticket, problems: [] });
    try {
      const res = await save.mutateAsync({ schema: draft, revision });
      own.record({ from: revision, to: res.group.revision });
      saved(ticket, res.group.schema, res.group.revision);
      toast.show(res.incomplete_members > 0 ? t.inventory.schemaTab.savedIncomplete(res.incomplete_members) : t.inventory.schemaTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStaleOn(ticket.generation);
      const ps = problemsFromError(err, 'body.schema');
      if (ps.length > 0) setReported(refusedWith(ticket, ps));
      else toast.show(err instanceof Error ? err.message : t.inventory.schemaTab.saveFailed, 'destructive');
    }
  }

  return (
    <Section
      id="schema"
      title={t.inventory.detail.section.schema}
      actions={
        canWrite ? (
          <div className="flex gap-2">
            <Button
              onClick={() => {
                discard();
                setReported(null);
              }}
            >
              {t.inventory.schemaTab.discard}
            </Button>
            <Button variant="primary" disabled={save.isPending} onClick={() => void onSave()}>
              {t.inventory.schemaTab.save}
            </Button>
          </div>
        ) : undefined
      }
    >
      {staleOn === generation && <StaleNotice onReload={onReload} />}
      <span className="text-xs text-muted-foreground">
        {t.inventory.schemaTab.summary(draft.fields.length, draft.fields.filter((f) => f.scope === 'host').length)}
      </span>
      {problems.length > 0 && (
        <p role="alert" className="text-xs text-destructive">
          {t.inventory.schemaTab.problems(problems.length)}
        </p>
      )}
      <VariableEditor key={generation} variant="group" value={draft} onChange={edit} problems={problems} readOnly={!canWrite} />
    </Section>
  );
}
