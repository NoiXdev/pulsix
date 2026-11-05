import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Section } from '../../../ui/detail-layout.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useReloadInventory, useSetGroupSchema } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { VariableEditor } from '../../variables/VariableEditor.tsx';
import { problemsFromError } from '../../variables/paths.ts';
import type { Problem, VarSchema } from '../../variables/types.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { useT } from '../../../i18n/useT.ts';

/** SchemaSection holds the group's variable schema editor (the full-width
 * VariableEditor, group variant: manual fields only). Discard and Save sit
 * in the section header for a writer; a reader gets the editor read-only,
 * so the fields still open, but in a dialog that saves nothing. */
export function SchemaSection({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const save = useSetGroupSchema(org, group.id);
  const [draft, setDraft] = useState<VarSchema>(() => toVarSchema(group.schema));
  const [problems, setProblems] = useState<Problem[]>([]);
  const [stale, setStale] = useState(false);
  // The editor keeps per-field state keyed by field identity (expanded
  // rows, sub-field lists); a wholesale reset of the draft (Discard) must
  // remount it, or it could keep showing state from before the reset.
  const [builderKey, setBuilderKey] = useState(0);

  const reload = useReloadInventory(org);

  // A section is keyed by what it edits, so a reload that brings other
  // content starts it over; one that does not keeps the draft, which then
  // saves on the reloaded revision.
  async function onReload() {
    await reload(group.id);
    setStale(false);
  }

  async function onSave() {
    setProblems([]);
    try {
      const res = await save.mutateAsync({ schema: draft, revision: group.revision });
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
                setDraft(toVarSchema(group.schema));
                setProblems([]);
                setBuilderKey((k) => k + 1);
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
      <VariableEditor key={builderKey} variant="group" value={draft} onChange={setDraft} problems={problems} readOnly={!canWrite} />
    </Section>
  );
}
