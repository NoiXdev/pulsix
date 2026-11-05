import { useRef, useState } from 'react';
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
  // A draft is built from stored values the same way on mount and after a
  // save: secrets as keep markers, never as anything typed.
  const draftOf = (vars: InventoryGroup['vars']) => toKeepMarkers(fields, declaredOnly(fields, (vars ?? {}) as Vars));
  const [draft, setDraft] = useState<Vars>(() => draftOf(group.vars));
  const [problems, setProblems] = useState<Problem[]>(() => (group.problems ?? []).map((p) => ({ path: p.path, message: p.message })));
  const [stale, setStale] = useState(false);
  // The revision a save sends as its If-Match. While the draft is pristine
  // (never edited) it follows group.revision: it then equals the stored
  // values at that revision (the section is keyed by what it edits; secrets
  // travel as keep markers), so saving on the newest revision overwrites
  // nothing — the user's own rename, say. The first edit pins it to the
  // revision the edit was made on, and a background refetch that brings a
  // newer revision with the same values no longer moves it: saving on that
  // revision would overwrite a change the user never saw (backlog 7.86). An
  // edit typed back to the start still counts as one. A successful save
  // rebuilds the draft from what it stored and makes it pristine again (a
  // pristine draft never holds a typed secret, which would otherwise ride
  // along on a later save past someone else's rotation); an edit typed while
  // that save was in flight is kept instead, pinned to the stored revision.
  // A Reload pins the draft to the reloaded revision and leaves it edited if
  // it was, since the kept draft still is not the stored values.
  const [touched, setTouched] = useState(false);
  const [pinnedRevision, setPinnedRevision] = useState(group.revision);
  // Pristine, it is the newer of the group's and the one this form's last
  // save stored, should the refetch after that save have failed.
  const revision = touched ? pinnedRevision : Math.max(pinnedRevision, group.revision);

  // Counts edits, so a save can tell whether one was typed while it was in
  // flight.
  const edits = useRef(0);

  function onChange(next: Vars) {
    edits.current += 1;
    if (!touched) {
      setPinnedRevision(revision);
      setTouched(true);
    }
    setDraft(next);
  }

  const reload = useReloadInventory(org);

  // A reload that brings other content starts the section over; one that
  // does not keeps the draft, which then saves on the reloaded revision.
  async function onReload() {
    const fresh = await reload(group.id);
    setPinnedRevision(fresh.revision);
    setStale(false);
  }

  async function onSave() {
    setProblems([]);
    const editsAtSave = edits.current;
    try {
      const res = await save.mutateAsync({ vars: declaredOnly(fields, draft), revision });
      setPinnedRevision(res.group.revision);
      if (edits.current === editsAtSave) {
        setDraft(draftOf(res.group.vars));
        setTouched(false);
      }
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
      <SchemaForm fields={fields} value={draft} onChange={onChange} problems={problems} disabled={!canWrite} idPrefix="values" />
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
