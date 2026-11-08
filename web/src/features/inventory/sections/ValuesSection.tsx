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
import { refusedWith, shownProblems, type Reported } from './draftProblems.ts';
import { useRevisionDraft } from './useRevisionDraft.ts';
import { useOwnSteps } from '../ownSteps.ts';
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

const toProblems = (ps: InventoryGroup['problems']): Problem[] => (ps ?? []).map((p) => ({ path: p.path, message: p.message }));

/** ValuesForm edits the group's values. The draft and the revision its save
 * sends follow useRevisionDraft (backlogs 7.86, 7.90): an unedited draft
 * follows the group, an edited one saves on the revision it was edited on
 * (or one the page's own writes moved it to). A save's stale notice and
 * the problems it was refused with show only beside the generation it was
 * started on, an answer's problems only beside the draft it rebuilt while
 * that draft is on the answer's revision (then the group's). Until a save
 * reports anything, and after a rebuild that no answer made, the form shows
 * the group's current problems, also beside an edit (backlog 7.91). */
function ValuesForm({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const t = useT();
  const toast = useToast();
  const fields = groupFields(toVarSchema(group.schema));
  const save = useSetGroupVars(org, group.id);
  const own = useOwnSteps();
  // A draft is built from stored values the same way on mount and after a
  // save: secrets as keep markers, never as anything typed.
  const draftOf = (vars: unknown) => toKeepMarkers(fields, declaredOnly(fields, (vars ?? {}) as Vars));
  const { draft, revision, generation, edit, startSave, saved, reloaded } = useRevisionDraft(group.vars, group.revision, draftOf, own.steps);
  // What was reported for a draft; nothing (the group's problems) at mount.
  const [reported, setReported] = useState<Reported | null>(null);
  // The problems a save's or reload's answer brought along, with the
  // revision it answered, for the draft it may replace: that answer, not the
  // group the page read last (which may land later), is the authority for
  // the content the draft then holds.
  const [answered, setAnswered] = useState<{ revision: number; problems: Problem[] } | null>(null);
  // The generation a save was refused as stale on.
  const [staleOn, setStaleOn] = useState<number | null>(null);
  // A draft replaced wholesale on the answer's revision shows that answer's
  // problems; any other replacement, the group's. An answer that kept the
  // draft (an edit typed meanwhile) is dropped.
  const [seen, setSeen] = useState(generation);
  if (seen !== generation || answered) {
    if (seen !== generation) {
      setReported(answered && answered.revision === revision ? { on: { generation, revision }, problems: answered.problems, answer: true } : null);
    }
    setSeen(generation);
    setAnswered(null);
  }

  const reload = useReloadInventory(org);

  // A reload that brings other content starts the draft over; one that
  // does not keeps it, which then saves on the reloaded revision.
  async function onReload() {
    const fresh = await reload(group.id);
    setAnswered({ revision: fresh.revision, problems: toProblems(fresh.problems) });
    reloaded(fresh.vars, fresh.revision);
    setStaleOn(null);
  }

  async function onSave() {
    const ticket = startSave();
    setReported({ on: ticket, problems: [], answer: false });
    try {
      const res = await save.mutateAsync({ vars: declaredOnly(fields, draft), revision });
      // The page's own step, which an edited draft may follow.
      own.record({ from: revision, to: res.group.revision });
      setAnswered({ revision: res.group.revision, problems: toProblems(res.group.problems) });
      saved(ticket, res.group.vars, res.group.revision);
      save.reset();
      toast.show(t.inventory.valuesTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStaleOn(ticket.generation);
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setReported(refusedWith(ticket, ps));
      else toast.show(err instanceof Error ? err.message : t.inventory.valuesTab.saveFailed, 'destructive');
    }
  }

  return (
    <div className="flex max-w-[640px] flex-col gap-4">
      {staleOn === generation && <StaleNotice onReload={onReload} />}
      {(group.orphaned ?? []).length > 0 && (
        <p className="rounded-md border border-border bg-card px-3 py-2 text-xs text-muted-foreground">
          {t.inventory.valuesTab.orphanedBefore}
          <span className="font-mono">{(group.orphaned ?? []).join(', ')}</span>
          {t.inventory.valuesTab.orphanedAfter}
        </p>
      )}
      {/* Inputs keep state of their own (a secret's stored marker, a
          repeater's entry keys); a draft replaced wholesale remounts them,
          as the section's key did before (backlog 7.90). */}
      <SchemaForm key={generation} fields={fields} value={draft} onChange={edit} problems={shownProblems(reported, { generation, revision }, toProblems(group.problems))} disabled={!canWrite} idPrefix="values" />
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
