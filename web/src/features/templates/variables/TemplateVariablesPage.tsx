import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { ArrowLeft } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useUnsavedGuard } from '../../../ui/useUnsavedGuard.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { may } from '../../../lib/roles.ts';
import { useOrg } from '../../../api/hooks/useOrgs.ts';
import { useTemplate } from '../../../api/hooks/useTemplates.ts';
import { useSetVariablesMode, useTemplateVariables, useUpdateTemplateVariables } from '../../../api/hooks/useTemplateVariables.ts';
import { useSchedules } from '../../../api/hooks/useSchedules.ts';
import { useTriggers } from '../../../api/hooks/useTriggers.ts';
import { toVarSchema } from '../../../api/hooks/useInventory.ts';
import type { Template, TemplateVariables } from '../../../api/types.ts';
import { VariableEditor } from '../../variables/VariableEditor.tsx';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { problemsFromError } from '../../variables/paths.ts';
import { sameValue } from '../../variables/origin.ts';
import { isKeepMarker, isSecretSet } from '../../variables/secrets.ts';
import { usageByField } from '../../variables/usage.ts';
import type { Problem, VarSchema, Vars } from '../../variables/types.ts';
import { TriggersDialog } from '../../triggers/TriggersDialog.tsx';
import { toDraftVars, varsForPut } from '../vars.ts';
import { keptNames } from './kept.ts';
import { ReviewBadges } from './ReviewBadges.tsx';
import { SourcePanel, type VarsMode } from './SourcePanel.tsx';
import { SyncPreviewDialog } from './SyncPreviewDialog.tsx';
import { useT } from '../../../i18n/useT.ts';
import { useConfirm } from '../../../ui/confirmContext.ts';

/**
 * TemplateVariablesPage is a job template's variables (backlog 7.75, plan
 * decision 3): where they come from and how they follow the playbook
 * (SourcePanel, the preview), the full-width field editor and the values
 * form of the active fields. The template's other settings stay in its
 * dialog.
 */
export function TemplateVariablesPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const orgInfo = useOrg(org);
  const template = useTemplate(org, id);
  const variables = useTemplateVariables(org, id);
  const templateGuard = useQueryGuard(template);
  const variablesGuard = useQueryGuard(variables);
  // The page body takes the variables as loaded when it mounts: its draft
  // and the revision the draft is based on. A background refetch (an
  // automatic sync moving the revision) must not move that base, or a save
  // would overwrite what it never saw; only a reload — after a save, an
  // apply, a mode change, or the stale notice — starts the body over.
  const [generation, setGeneration] = useState(0);

  if (templateGuard) return templateGuard;
  if (variablesGuard) return variablesGuard;
  if (!template.data || !variables.data) return <QueryLoading />;

  async function reload() {
    await Promise.all([variables.refetch(), template.refetch()]);
    setGeneration((g) => g + 1);
  }

  return (
    <VariablesBody
      key={generation}
      org={org}
      template={template.data.template}
      data={variables.data}
      role={orgInfo.data?.org.role}
      onReload={reload}
    />
  );
}

interface BodyProps {
  org: string;
  template: Template;
  /** The variables as last read; the draft starts from them at mount. */
  data: TemplateVariables;
  role?: string;
  onReload: () => Promise<void>;
}

function VariablesBody({ org, template, data, role, onReload }: BodyProps) {
  const t = useT();
  const v = t.templates.variables;
  const toast = useToast();
  const ask = useConfirm();
  const projected = Boolean(template.shared_template_id);
  const canWrite = may(role, 'templates.write') && !projected;
  const [loaded] = useState(() => {
    const schema = toVarSchema(data.extra_vars_schema);
    const hasSchema = Boolean(data.extra_vars_schema);
    return {
      revision: data.revision,
      hasSchema,
      schema,
      vars: hasSchema ? toDraftVars(schema.fields, data.extra_vars) : data.extra_vars,
      kept: hasSchema ? keptNames(schema.fields, data.extra_vars) : [],
    };
  });
  const [draft, setDraft] = useState<VarSchema>(loaded.schema);
  const [values, setValues] = useState<Vars>(loaded.vars);
  const [editorKey, setEditorKey] = useState(0);
  const [schemaProblems, setSchemaProblems] = useState<Problem[]>([]);
  const [valueProblems, setValueProblems] = useState<Problem[]>([]);
  const [stale, setStale] = useState(false);
  // Set once a save went through: the body is about to be read again and
  // remounted on the new revision, and a second save before that would be
  // based on the old one (412).
  const [saved, setSaved] = useState(false);
  const [syncing, setSyncing] = useState<'manual' | 'automatic' | null>(null);
  const [webhooks, setWebhooks] = useState(false);
  const save = useUpdateTemplateVariables(org, template.id);
  const setMode = useSetVariablesMode(org, template.id);
  const schedules = useSchedules(org, template.id);
  const target = { kind: 'template_run' as const, id: template.id, name: template.name };
  const triggers = useTriggers(org, target);
  const usage = usageByField(schedules.data?.schedules ?? [], triggers.data?.triggers ?? []);
  const dirty = !sameValue(draft, loaded.schema) || !sameValue(values, loaded.vars);
  // A link (the back link, a schedule under "Used in") asks first; not
  // after a save, whose draft the reload is about to replace.
  useUnsavedGuard(dirty && !saved);

  async function onSave() {
    setSchemaProblems([]);
    setValueProblems([]);
    // A template without a schema keeps none until a field is added; one
    // with a schema keeps it (empty, too), so the values stored under its
    // removed fields stay (the server drops them with the schema).
    const withSchema = loaded.hasSchema || draft.fields.length > 0;
    try {
      await save.mutateAsync({
        schema: withSchema ? draft : undefined,
        vars: varsForPut(withSchema ? draft.fields : undefined, values),
        revision: loaded.revision,
      });
      setSaved(true);
      toast.show(v.saved);
      await onReload();
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      const schemaPs = problemsFromError(err, 'body.extra_vars_schema');
      const valuePs = problemsFromError(err, 'body.extra_vars');
      setSchemaProblems(schemaPs);
      setValueProblems(valuePs);
      if (schemaPs.length + valuePs.length === 0) toast.show(err instanceof Error ? err.message : v.saveFailed, 'destructive');
    }
  }

  function discard() {
    setDraft(loaded.schema);
    setValues(loaded.vars);
    setSchemaProblems([]);
    setValueProblems([]);
    setEditorKey((k) => k + 1);
  }

  async function onMode(next: VarsMode) {
    if (next === data.mode) return;
    // Linking goes through the preview: the apply links, never a mode write.
    if (data.mode === 'off') {
      if (next !== 'off') setSyncing(next);
      return;
    }
    if (next === 'off' && !(await ask({ message: t.templates.source.confirmOff }))) return;
    try {
      await setMode.mutateAsync({ mode: next, revision: loaded.revision });
      await onReload();
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      toast.show(err instanceof Error ? err.message : t.templates.source.modeFailed, 'destructive');
    }
  }

  const problemCount = schemaProblems.length + valueProblems.length;
  // A removed secret added back under another type: its stored value is
  // sealed and cannot be shown or kept there (varsForPut leaves it out).
  const secretNotes: Problem[] = draft.fields
    .filter((f) => f.type !== 'secret' && (isKeepMarker(values[f.name]) || isSecretSet(values[f.name])))
    .map((f) => ({ path: f.name, message: v.secretUnderType }));
  // What a run leaves out as stored: a secret under a field that is no
  // longer a secret, until a person enters its value again (backlog 7.87).
  // The review is absent on a write's answer; its list is never null.
  const notSent = data.review ? data.review.not_sent : [];
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <div className="flex flex-col gap-2">
        <Link
          to="/o/$org/templates"
          params={{ org }}
          className="inline-flex w-fit items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <ArrowLeft size={13} /> {v.back}
        </Link>
        <h1 className="text-xl font-semibold">{v.title(template.name)}</h1>
        <ReviewBadges review={data.review} />
      </div>
      {stale && (
        <div role="alert" className="flex items-center justify-between gap-3 rounded-md border border-border bg-card px-4 py-3 text-xs">
          <span>{v.stale}</span>
          <Button type="button" onClick={() => void onReload()}>
            {v.reload}
          </Button>
        </div>
      )}
      {projected && <p className="text-xs text-muted-foreground">{v.projected}</p>}
      <SourcePanel
        org={org}
        source={data.source}
        mode={data.mode}
        canWrite={canWrite}
        canSyncRepo={may(role, 'repos.write')}
        lockedReason={dirty ? v.dirty : undefined}
        onMode={(m) => void onMode(m)}
        onSyncNow={() => data.mode !== 'off' && setSyncing(data.mode)}
      />
      <section aria-label={v.fields} className="flex flex-col gap-2">
        <h2 className="text-sm font-semibold">{v.fields}</h2>
        <VariableEditor
          key={editorKey}
          variant="template"
          value={draft}
          onChange={setDraft}
          problems={schemaProblems}
          usage={usage}
          usageLinks={{ org, onOpenWebhooks: () => setWebhooks(true) }}
          readOnly={!canWrite}
        />
      </section>
      <section aria-label={v.values} className="flex flex-col gap-2">
        <h2 className="text-sm font-semibold">{v.values}</h2>
        <span className="text-[11px] text-muted-foreground">{v.valuesHint}</span>
        {notSent.length > 0 && (
          <p role="status" className="rounded-md border border-[var(--px-warn)] bg-card px-4 py-3 text-xs text-[var(--px-warn)]">
            {v.notSent(notSent.join(', '))}
          </p>
        )}
        <SchemaForm fields={draft.fields} value={values} onChange={setValues} problems={[...valueProblems, ...secretNotes]} idPrefix="template-vars" disabled={!canWrite} />
        {loaded.kept.length > 0 && (
          <div className="flex flex-col gap-0.5 text-[11px] text-muted-foreground">
            <span>{v.kept(loaded.kept.join(', '))}</span>
            <span>{v.keptHint}</span>
          </div>
        )}
      </section>
      {canWrite && (
        <div className="sticky bottom-0 z-10 -mx-1 flex items-center justify-end gap-2 border-t border-border bg-background px-1 py-3">
          {problemCount > 0 && (
            <p role="alert" className="mr-auto text-xs text-destructive">
              {v.problems(problemCount)}
            </p>
          )}
          <Button type="button" disabled={!dirty || saved} onClick={discard}>
            {v.discard}
          </Button>
          <Button type="button" variant="primary" disabled={save.isPending || saved} onClick={() => void onSave()}>
            {v.save}
          </Button>
        </div>
      )}
      {syncing && (
        <SyncPreviewDialog
          org={org}
          templateId={template.id}
          revision={loaded.revision}
          mode={syncing}
          onClose={() => setSyncing(null)}
          onApplied={() => {
            setSyncing(null);
            toast.show(t.templates.preview.applied);
            void onReload();
          }}
          onReload={() => {
            setSyncing(null);
            void onReload();
          }}
        />
      )}
      {webhooks && (
        <TriggersDialog org={org} target={target} canWrite={canWrite} canRun={may(role, 'jobs.run')} onClose={() => setWebhooks(false)} />
      )}
    </div>
  );
}
