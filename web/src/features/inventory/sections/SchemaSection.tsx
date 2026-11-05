import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useReloadInventory, useSetGroupSchema } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { VariableEditor } from '../../variables/VariableEditor.tsx';
import { problemsFromError } from '../../variables/paths.ts';
import type { Problem } from '../../variables/types.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { useRevisionDraft } from './useRevisionDraft.ts';
import { useOwnSteps } from '../ownSteps.ts';
import { useT } from '../../../i18n/useT.ts';

/** SchemaSection holds the group's variable schema editor (the full-width
 * VariableEditor, group variant: manual fields only). Discard and Save sit
 * in the section header for a writer; a reader gets the editor read-only,
 * so the fields still open, but in a dialog that saves nothing. The draft
 * and the revision its save sends follow useRevisionDraft (backlog 7.89):
 * an unedited draft follows the group, an edited one saves on the revision
 * it was edited on (or one the page's own writes moved it to). */
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
  const [problems, setProblems] = useState<Problem[]>([]);
  const [stale, setStale] = useState(false);
  // A draft replaced wholesale is not the one the problems were found in.
  const [problemsOf, setProblemsOf] = useState(generation);
  if (problemsOf !== generation) {
    setProblemsOf(generation);
    setProblems([]);
  }

  const reload = useReloadInventory(org);

  // A reload that brings other content starts the draft over; one that
  // does not keeps it, which then saves on the reloaded revision.
  async function onReload() {
    const fresh = await reload(group.id);
    reloaded(fresh.schema, fresh.revision);
    setStale(false);
  }

  async function onSave() {
    setProblems([]);
    const ticket = startSave();
    try {
      const res = await save.mutateAsync({ schema: draft, revision });
      own.record({ from: revision, to: res.group.revision });
      saved(ticket, res.group.schema, res.group.revision);
      toast.show(res.incomplete_members > 0 ? t.inventory.schemaTab.savedIncomplete(res.incomplete_members) : t.inventory.schemaTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      const ps = problemsFromError(err, 'body.schema');
      if (ps.length > 0) setProblems(ps);
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
                setProblems([]);
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
      {stale && <StaleNotice onReload={onReload} />}
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
