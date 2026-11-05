import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { DisabledHint, Section } from '../../../ui/detail-layout.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useReloadInventory, useSetGroupVars } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { groupFields, problemsFromError } from '../../variables/paths.ts';
import { declaredOnly, toKeepMarkers } from '../../variables/secrets.ts';
import type { Problem, Vars } from '../../variables/types.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { useT } from '../../../i18n/useT.ts';

/** ValuesSection is the form for the group's own (group-scoped) values. A
 * reader sees it disabled and without its save button; a schema without any
 * group field gets a hint in its place. */
export function ValuesSection({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const v = t.inventory.valuesTab;
  return (
    <Section id="values" title={t.inventory.detail.section.values}>
      {groupFields(toVarSchema(group.schema)).length === 0 ? (
        <DisabledHint
          title={v.noFieldsTitle}
          body={v.noFieldsBody}
          action={
            canWrite ? (
              <a href="#schema" className="text-[13px] text-primary hover:underline">
                {v.toSchema}
              </a>
            ) : undefined
          }
        />
      ) : (
        <ValuesForm org={org} group={group} canWrite={canWrite} />
      )}
    </Section>
  );
}

function ValuesForm({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const fields = groupFields(toVarSchema(group.schema));
  const save = useSetGroupVars(org, group.id);
  const [draft, setDraft] = useState<Vars>(() => toKeepMarkers(fields, declaredOnly(fields, (group.vars ?? {}) as Vars)));
  const [problems, setProblems] = useState<Problem[]>(() => (group.problems ?? []).map((p) => ({ path: p.path, message: p.message })));
  const [stale, setStale] = useState(false);

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
      await save.mutateAsync({ vars: declaredOnly(fields, draft), revision: group.revision });
      save.reset();
      toast.show(t.inventory.valuesTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setProblems(ps);
      else toast.show(err instanceof Error ? err.message : t.inventory.valuesTab.saveFailed, 'destructive');
    }
  }

  return (
    <div className="flex max-w-[640px] flex-col gap-4">
      {stale && <StaleNotice onReload={onReload} />}
      {(group.orphaned ?? []).length > 0 && (
        <p className="rounded-md border border-border bg-card px-3 py-2 text-xs text-muted-foreground">
          {t.inventory.valuesTab.orphanedBefore}
          <span className="font-mono">{(group.orphaned ?? []).join(', ')}</span>
          {t.inventory.valuesTab.orphanedAfter}
        </p>
      )}
      <SchemaForm fields={fields} value={draft} onChange={setDraft} problems={problems} disabled={!canWrite} idPrefix="values" />
      {canWrite && (
        <div className="flex justify-end">
          <Button variant="primary" disabled={save.isPending} onClick={() => void onSave()}>
            {t.inventory.valuesTab.save}
          </Button>
        </div>
      )}
    </div>
  );
}
