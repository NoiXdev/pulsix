import { useEffect, useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Tag } from '../../../ui/badge.tsx';
import { DisabledHint } from '../../../ui/detail-layout.tsx';
import { CardSaveBar, SettingsCard } from '../../../ui/settings.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useInventoryPreview, useReloadInventory, useSetGroupVars } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { activeFields } from '../../variables/origin.ts';
import { groupFields, problemsFromError } from '../../variables/paths.ts';
import { declaredOnly, toKeepMarkers } from '../../variables/secrets.ts';
import type { Problem, SchemaField, Vars } from '../../variables/types.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { refusedWith, shownProblems, type Reported } from './draftProblems.ts';
import { useRevisionDraft } from './useRevisionDraft.ts';
import { useOwnSteps } from '../ownSteps.ts';
import { ancestorVarsFrom, formatValue, provenanceOf, type AncestorVars } from '../provenance.ts';
import { ancestorsOf } from '../tree.ts';
import { useT } from '../../../i18n/useT.ts';

interface VariablesTabProps {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  canWrite: boolean;
  /** Opens the group's fields, from the hint a schema without group fields gets. */
  onManageFields: () => void;
  /** Told whether the form holds an edit not saved yet (false for a
   * reader), for the page's leave question. */
  onDirtyChange?: (dirty: boolean) => void;
}

/** VariablesTab holds the group's own (group-scoped) values, one row per
 * field. A reader sees them disabled and without the save bar; a schema
 * without any group field gets a hint in their place. */
export function VariablesTab({ org, group, groups, canWrite, onManageFields, onDirtyChange }: VariablesTabProps) {
  const t = useT();
  const v = t.inventory.valuesTab;
  if (groupFields(toVarSchema(group.schema)).length === 0) {
    return (
      <DisabledHint
        title={v.noFieldsTitle}
        body={v.noFieldsBody}
        action={canWrite ? <Button onClick={onManageFields}>{t.inventory.groupPage.manageFields}</Button> : undefined}
      />
    );
  }
  return <ValuesForm org={org} group={group} groups={groups} canWrite={canWrite} onDirtyChange={onDirtyChange} />;
}

const toProblems = (ps: InventoryGroup['problems']): Problem[] => (ps ?? []).map((p) => ({ path: p.path, message: p.message }));

const FORM_ID = 'group-values-form';

/** ValuesForm edits the group's values. The draft and the revision its save
 * sends follow useRevisionDraft (backlogs 7.86, 7.90): an unedited draft
 * follows the group, an edited one saves on the revision it was edited on
 * (or one the page's own writes moved it to). A save's stale notice and
 * the problems it was refused with show only beside the generation it was
 * started on (until a later save of that draft succeeds), an answer's
 * problems only beside the draft it rebuilt while that draft is on the
 * answer's revision (then the group's). Until a save
 * reports anything, and after a rebuild that no answer made, the form shows
 * the group's current problems, also beside an edit (backlog 7.91). Save
 * waits for an edit, or for stored values of removed fields, which a save
 * drops (as the notice above the form says); Discard starts the draft over
 * from the group. */
function ValuesForm({ org, group, groups, canWrite, onDirtyChange }: Omit<VariablesTabProps, 'onManageFields'>) {
  const t = useT();
  const toast = useToast();
  const fields = groupFields(toVarSchema(group.schema));
  const save = useSetGroupVars(org, group.id);
  const own = useOwnSteps();
  // Where each value comes from is read off the preview, which renders the
  // ancestors' vars as Ansible gets them. The group's own preview renders
  // only the groups above a managed host at or below it; when it lacks an
  // ancestor, the organization's is read instead (as the Inheritance tab
  // does).
  const path = ancestorsOf(groups, group.id);
  const scoped = useInventoryPreview(org, group.id);
  const scopedAncestors = ancestorVarsFrom(scoped.data?.inventory, path);
  const needWhole = !!scoped.data && scopedAncestors.some((a) => a.vars === undefined);
  const whole = useInventoryPreview(org, undefined, { enabled: needWhole });
  const ancestors = needWhole ? ancestorVarsFrom(whole.data?.inventory, path) : scopedAncestors;
  // A draft is built from stored values the same way on mount and after a
  // save: secrets as keep markers, never as anything typed.
  const draftOf = (vars: unknown) => toKeepMarkers(fields, declaredOnly(fields, (vars ?? {}) as Vars));
  const { draft, revision, generation, touched, edit, startSave, saved, reloaded, discard } = useRevisionDraft(group.vars, group.revision, draftOf, own);
  const orphaned = group.orphaned ?? [];
  // Stored values of removed fields are something to save too: a save
  // drops them. Only an edit counts for the leave question, though.
  const savable = touched || orphaned.length > 0;
  const edited = canWrite && touched;
  useEffect(() => {
    onDirtyChange?.(edited);
  }, [edited, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);
  // What was reported for a draft; nothing (the group's problems) at mount.
  const [reported, setReported] = useState<Reported | null>(null);
  // The problems a save's or reload's answer brought along, with the
  // revision it answered, for the draft it may replace: that answer, not the
  // group the page read last (which may land later), is the authority for
  // the content the draft then holds.
  const [answered, setAnswered] = useState<{ revision: number; problems: Problem[] } | null>(null);
  // The generation a save was refused as stale on; a later save that
  // succeeds clears it.
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
      own.record({ from: revision, to: res.group.revision, kind: 'content' });
      setAnswered({ revision: res.group.revision, problems: toProblems(res.group.problems) });
      saved(ticket, res.group.vars, res.group.revision);
      setStaleOn(null);
      save.reset();
      toast.show(t.inventory.valuesTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStaleOn(ticket.generation);
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setReported(refusedWith(ticket, ps));
      else toast.show(err instanceof Error ? err.message : t.inventory.valuesTab.saveFailed, 'destructive');
    }
  }

  const problems = shownProblems(reported, { generation, revision }, toProblems(group.problems));
  const shown = activeFields(fields);

  return (
    <SettingsCard
      title={t.inventory.detail.section.values}
      footer={
        canWrite ? (
          <CardSaveBar
            dirty={savable}
            saving={save.isPending}
            formId={FORM_ID}
            saveLabel={t.inventory.valuesTab.save}
            onDiscard={() => {
              discard();
              setReported(null);
            }}
          />
        ) : undefined
      }
    >
      {staleOn === generation && (
        <div className="px-5 pb-3.5">
          <StaleNotice onReload={onReload} />
        </div>
      )}
      {orphaned.length > 0 && (
        <p className="mx-5 mb-3.5 rounded-md border border-border bg-background px-3 py-2 text-xs text-muted-foreground">
          {t.inventory.valuesTab.orphanedBefore}
          <span className="font-mono">{orphaned.join(', ')}</span>
          {t.inventory.valuesTab.orphanedAfter}
        </p>
      )}
      <form
        id={FORM_ID}
        onSubmit={(e) => {
          e.preventDefault();
          if (canWrite && savable && !save.isPending) void onSave();
        }}
        className="flex flex-col"
      >
        {shown.length === 0 && <p className="border-t border-border px-5 py-3.5 text-xs text-muted-foreground">{t.variables.form.noFields}</p>}
        {/* Inputs keep state of their own (a secret's stored marker, a
            repeater's entry keys); a draft replaced wholesale remounts them,
            as the section's key did before (backlog 7.90). */}
        {shown.map((f) => (
          <VariableRow key={`${generation}:${f.name}`} field={f} draft={draft} onChange={edit} problems={problems} canWrite={canWrite} ancestors={ancestors} />
        ))}
      </form>
    </SettingsCard>
  );
}

/** One variable: its key and type, its input, and where its effective
 * value comes from when the data at hand says. */
function VariableRow({
  field, draft, onChange, problems, canWrite, ancestors,
}: {
  field: SchemaField;
  draft: Vars;
  onChange: (next: Vars) => void;
  problems: readonly Problem[];
  canWrite: boolean;
  ancestors: readonly AncestorVars[];
}) {
  const t = useT();
  const v = t.inventory.groupPage.variables;
  const types = t.inventory.fields.types;
  const from = provenanceOf(field, draft[field.name], ancestors);
  let line: string | null = null;
  if (from?.kind === 'overrides') line = v.overrides(from.group, formatValue(from.value));
  else if (from?.kind === 'inherits') line = v.inherits(formatValue(from.value), from.group);
  else if (from?.kind === 'default') line = v.schemaDefault(formatValue(from.value));

  return (
    <div data-testid={`variable-${field.name}`} className="grid gap-x-6 gap-y-1.5 border-t border-border px-5 py-3.5 sm:grid-cols-[minmax(0,200px)_minmax(0,1fr)]">
      <div className="flex min-w-0 flex-wrap items-center gap-1.5 self-start sm:pt-1.5">
        <span aria-hidden="true" className="font-mono text-[13px] font-semibold break-all">
          {field.name}
        </span>
        <Tag>{Object.hasOwn(types, field.type) ? types[field.type as keyof typeof types].label : field.type}</Tag>
        {field.required && <span className="text-[11px] text-destructive">{v.required}</span>}
        {field.label && <span className="w-full text-[12px] text-muted-foreground">{field.label}</span>}
      </div>
      <div className="flex min-w-0 flex-col gap-1.5">
        <SchemaForm fields={[field]} value={draft} onChange={onChange} problems={problems} disabled={!canWrite} idPrefix="values" hideLabels />
        {line && <p className="text-[12px] text-muted-foreground">{line}</p>}
      </div>
    </div>
  );
}
