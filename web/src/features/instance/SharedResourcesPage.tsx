import { useState, type ChangeEvent, type FormEvent } from 'react';
import { Activity, GitBranch, ListPlus, Trash2 } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SchemaBuilder } from '../inventory/schema/SchemaBuilder.tsx';
import { SchemaForm } from '../inventory/schema/SchemaForm.tsx';
import { toVarSchema } from '../../api/hooks/useInventory.ts';
import type { FieldType, VarSchema, Vars } from '../inventory/schema/types.ts';
import {
  useCreateRepoDefinition,
  useDeleteRepoDefinition,
  useRepoDefinitions,
  useUpdateRepoDefinition,
} from '../../api/hooks/useSharedRepoDefinitions.ts';
import {
  useCreateTemplateDefinition,
  useDeleteTemplateDefinition,
  useTemplateDefinitions,
  useUpdateTemplateDefinition,
} from '../../api/hooks/useSharedTemplateDefinitions.ts';
import {
  useCheckDefinitions,
  useCreateCheckDefinition,
  useDeleteCheckDefinition,
  useUpdateCheckDefinition,
} from '../../api/hooks/useSharedCheckDefinitions.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import type {
  CheckDefinitionDef,
  CheckDefinitionDefInput,
  RepoDefinition,
  RepoDefinitionInput,
  TemplateDefinition,
  TemplateDefinitionInput,
} from '../../api/types.ts';

const REPO_COLUMNS = 'minmax(0, 0.8fr) minmax(0, 1.3fr) 110px minmax(0, 0.9fr) 90px';
const TEMPLATE_COLUMNS = 'minmax(0, 0.8fr) minmax(0, 0.8fr) minmax(0, 1.1fr) 110px 90px';
const CHECK_COLUMNS = 'minmax(0, 0.8fr) minmax(0, 0.6fr) minmax(0, 1fr) 90px';

/**
 * SharedResourcesPage is where an instance admin defines the provider's own
 * resources once, for every organization (spec v0.2f, decisions 1-8): a
 * repository's name/url/branch/subpath (Task 5) and — Task 6's own
 * addition, this section — a job template's name, the shared repository its
 * playbook resolves against, the playbook's path, limit and extra vars.
 * Both are projected into every organization as an ordinary,
 * read-only-in-those-fields row (RepoDialog.tsx / TemplateDialog.tsx) —
 * each organization still supplies its own binding (a repository's
 * credential, a template's scope and SSH credential), since neither ever
 * belongs to a definition (decisions 2, 3).
 *
 * Reachable with Principal.InstanceAdmin only, the same gate
 * SmtpSettingsPage's /admin/settings/smtp already uses: GET/POST/PUT/DELETE
 * /admin/{repos,templates}/definitions admit no other caller, so a non-admin
 * reaching this page (its tab is hidden from them, but the route itself is
 * not guarded client-side) sees the 403 QueryError page instead of either
 * list. It lives under an organization's Settings only for navigation,
 * exactly as SmtpSettingsPage's own doc comment says of itself — the
 * definitions are instance-wide, not this organization's.
 *
 * Three independent sections, not one combined list: Task 5's own report
 * named this the extension point ("the title 'Shared resources' vs. the one
 * table's own 'repository definitions' framing already leaves room for
 * that") rather than a page restructure, and that held through Task 6
 * (job templates) and now Task 7 (check definitions) — RepoDefinitions,
 * TemplateDefinitions and CheckDefinitions below are self-contained, each
 * with its own query, dialog and delete button, composed here rather than
 * sharing state.
 */
export function SharedResourcesPage() {
  const t = useT();
  return (
    <>
      <h1 className="text-xl font-semibold">{t.settings.tabs.sharedResources}</h1>
      <RepoDefinitionsSection />
      <TemplateDefinitionsSection />
      <CheckDefinitionsSection />
    </>
  );
}

/** RepoDefinitionsSection is SharedResourcesPage's original (Task 5) body,
 * unchanged, pulled into its own component so a second, independent section
 * could be added beside it without restructuring either. */
function RepoDefinitionsSection() {
  const t = useT();
  const definitions = useRepoDefinitions();
  const [dialog, setDialog] = useState<'new' | RepoDefinition | null>(null);

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  return (
    <>
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.instance.shared.repos.count(list.length)}
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <GitBranch size={15} /> {t.instance.shared.repos.add}
        </Button>
      </div>
      <Card>
        <GridHeader columns={REPO_COLUMNS}>
          <span>{t.instance.shared.name}</span>
          <span>{t.instance.shared.repos.url}</span>
          <span>{t.instance.shared.repos.branch}</span>
          <span>{t.instance.shared.repos.subpath}</span>
          <span>{t.instance.shared.actions}</span>
        </GridHeader>
        {list.map((d) => (
          <GridRow key={d.id} columns={REPO_COLUMNS}>
            <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(d)}>
              {d.name}
            </button>
            <span className="truncate font-mono text-xs text-muted-foreground">{d.url}</span>
            <span className="truncate font-mono text-xs">{d.branch}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{d.subpath || '—'}</span>
            <DeleteDefinitionButton definition={d} />
          </GridRow>
        ))}
        {list.length === 0 && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.instance.shared.repos.empty}</p>
        )}
      </Card>
      {dialog === 'new' ? (
        <RepoDefinitionDialog onClose={() => setDialog(null)} />
      ) : dialog ? (
        <RepoDefinitionDialog definition={dialog} onClose={() => setDialog(null)} />
      ) : null}
    </>
  );
}

/**
 * DeleteDefinitionButton is its own component, not an inline handler on the
 * row, only so the mutation's pending state belongs to the one row it
 * disables rather than to the whole list (ReposPage's own SyncButton is the
 * same split, for the same reason).
 *
 * A definition a schedule, trigger or playbook still references refuses
 * with 409 (spec decision 5), naming the organization and what references
 * it — mapSharedErr composes that sentence server-side specifically so an
 * instance admin's own screen can show it verbatim; this just surfaces
 * err.message rather than a generic "deleting failed", which is the one
 * thing that would throw the detail away.
 */
function DeleteDefinitionButton({ definition }: { definition: RepoDefinition }) {
  const t = useT();
  const toast = useToast();
  const remove = useDeleteRepoDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={t.instance.shared.deleteAria(definition.name)}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(t.instance.shared.repos.confirmDelete(definition.name))) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(t.instance.shared.repos.deleted(definition.name));
          } catch (err) {
            toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
          }
        }}
      >
        <Trash2 size={13} />
      </Button>
    </span>
  );
}

interface DefinitionFormValues {
  name: string;
  url: string;
  branch: string;
  subpath: string;
}

/**
 * validateDefinitionInput mirrors repoDefinitionBody's own bounds
 * (internal/api/shared_handlers.go: minLength/maxLength only, no pattern) —
 * unlike features/repos/validate.ts, which mirrors internal/repos/
 * service.go's own regex checks on an organization's repository.
 * shared.RepoDefinitionInput deliberately enforces no format beyond huma's
 * own schema layer (see shared_handlers.go's own comment on
 * repoDefinitionBody), so reusing repos/validate.ts's stricter patterns
 * here would reject input the server would accept — this stays only as
 * strict as the server actually is.
 */
function validateDefinitionInput(
  values: DefinitionFormValues,
  t: Messages,
): Partial<Record<keyof DefinitionFormValues, string>> {
  const errors: Partial<Record<keyof DefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = t.validation.required;
  else if (values.name.length > 63) errors.name = t.validation.maxChars(63);
  if (values.url.trim() === '') errors.url = t.validation.required;
  else if (values.url.length > 500) errors.url = t.validation.maxChars(500);
  if (values.branch.length > 100) errors.branch = t.validation.maxChars(100);
  if (values.subpath.length > 200) errors.subpath = t.validation.maxChars(200);
  return errors;
}

/**
 * RepoDefinitionDialog creates or edits one installation-owned repository
 * definition. No credential field — decision 3 keeps every credential
 * inside the organization that supplies it, never on the definition a
 * worker in any organization's zone could reach.
 */
function RepoDefinitionDialog({ definition, onClose }: { definition?: RepoDefinition; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const labels: Record<keyof DefinitionFormValues, string> = {
    name: t.instance.shared.name,
    url: t.instance.shared.repos.url,
    branch: t.instance.shared.repos.branch,
    subpath: t.instance.shared.repos.subdirectory,
  };
  const create = useCreateRepoDefinition();
  const update = useUpdateRepoDefinition(definition?.id ?? '');
  const [values, setValues] = useState<DefinitionFormValues>(
    definition
      ? { name: definition.name, url: definition.url, branch: definition.branch, subpath: definition.subpath }
      : { name: '', url: '', branch: '', subpath: '' },
  );
  const [errors, setErrors] = useState<Partial<Record<keyof DefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function setValue(key: keyof DefinitionFormValues) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: keyof DefinitionFormValues, placeholder: string) {
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`repo-def-${key}`}>{labels[key]}</Label>
        <Input
          id={`repo-def-${key}`}
          className="font-mono"
          value={values[key]}
          onChange={setValue(key)}
          placeholder={placeholder}
          aria-invalid={errors[key] ? true : undefined}
          autoFocus={key === 'name'}
        />
        {errors[key] && (
          <span role="alert" className="text-[11px] text-destructive">
            {errors[key]}
          </span>
        )}
      </div>
    );
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError(null);
    const fieldErrors = validateDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    const body: RepoDefinitionInput = { name: values.name, url: values.url, branch: values.branch, subpath: values.subpath };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.repos.saved(body.name) : t.instance.shared.repos.created(body.name),
      );
      onClose();
    } catch (err) {
      // Shown whole, not routed to one field: decision 7's collision
      // message names every colliding organization and decision 5's
      // in-use message names one plus what still references it — neither
      // is about a single field the way features/repos/validate.ts's own
      // server-mirror errors are.
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition?.name ?? t.instance.shared.repos.newTitle}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {field('name', 'ansible-base')}
        {field('url', 'https://git.example/provider/ansible-base.git')}
        {field('branch', 'main')}
        {field('subpath', t.instance.shared.repos.subpathPlaceholder)}
        <p className="text-[11px] text-muted-foreground">{t.instance.shared.repos.hint}</p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// --- job template definitions (Task 6) ---
//
// Task 6's own application of RepoDefinitionsSection's own shape above: a
// job template definition's shared half (spec decision 2) is name, the
// shared repository its playbook resolves against, the playbook's path
// within that repository, and limit — read-only inside every organization
// once projected (TemplateDialog.tsx's own `projected` guard). Extra vars
// and their schema are deliberately NOT edited here: shared.Service already
// refuses a schema that declares a secret field (schemaHasSecretField,
// internal/shared/definition.go — "a shared definition carries no secret by
// construction"), and authoring a typed schema is SchemaBuilder's own,
// sizeable feature (features/inventory/schema/SchemaBuilder.tsx) that nothing
// here needs to re-host: a definition created without one, then given one
// through a follow-up PUT this dialog does not need to produce, still works
// — the server defaults extra_vars to {} and the schema to absent either
// way, on CREATE. Both PUTs are full replaces, though, and a review found
// the gap that comment did not cover: submit() used to build its body from
// only the fields this form renders, so EDITING the limit (or any other
// rendered field) silently cleared extra vars and their schema a provider
// had set through the API, for every organization's projection, in the
// same transaction. TemplateDefinitionDialog below now rounds both back
// through the form's submitted body unchanged instead of defaulting them —
// still not editable here (that is backlog work, not this fix), but no
// longer destroyed by an edit that never touched them.

/** DeleteDefinitionButton's own doc comment, same reasoning, different
 * table: a schedule, trigger or (for a template) nothing-else-new blocks
 * with 409 naming the organization — DeleteTemplateWithProjections' own
 * inventory (internal/shared/store_sql.go) is schedules and job triggers
 * only; job_template_vault_credentials cascades on purpose and never
 * appears in this message. */
function DeleteTemplateDefinitionButton({ definition }: { definition: TemplateDefinition }) {
  const t = useT();
  const toast = useToast();
  const remove = useDeleteTemplateDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={t.instance.shared.deleteAria(definition.name)}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(t.instance.shared.templates.confirmDelete(definition.name))) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(t.instance.shared.templates.deleted(definition.name));
          } catch (err) {
            toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
          }
        }}
      >
        <Trash2 size={13} />
      </Button>
    </span>
  );
}

function TemplateDefinitionsSection() {
  const t = useT();
  const definitions = useTemplateDefinitions();
  const repoDefinitions = useRepoDefinitions();
  const [dialog, setDialog] = useState<'new' | TemplateDefinition | null>(null);

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];
  // A template definition always names a shared repository (migration
  // 00061's own reason shared_repo_id is NOT NULL: a playbook path alone
  // names nothing without the repository it is relative to) — so the
  // picker's options come from the OTHER section's own query. An empty
  // list here is a legitimate state (no repository definition exists yet),
  // not an error; the dialog's own validation refuses to submit without one.
  const repoOptions = (repoDefinitions.data?.definitions ?? []).map((d) => ({ value: d.id, label: d.name }));

  return (
    <>
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.instance.shared.templates.count(list.length)}
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <ListPlus size={15} /> {t.instance.shared.templates.add}
        </Button>
      </div>
      <Card>
        <GridHeader columns={TEMPLATE_COLUMNS}>
          <span>{t.instance.shared.name}</span>
          <span>{t.instance.shared.templates.repository}</span>
          <span>{t.instance.shared.templates.playbookPath}</span>
          <span>{t.instance.shared.templates.limit}</span>
          <span>{t.instance.shared.actions}</span>
        </GridHeader>
        {list.map((d) => (
          <GridRow key={d.id} columns={TEMPLATE_COLUMNS}>
            <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(d)}>
              {d.name}
            </button>
            <span className="truncate font-mono text-xs text-muted-foreground">
              {repoOptions.find((r) => r.value === d.shared_repo_id)?.label ?? d.shared_repo_id.slice(0, 8)}
            </span>
            <span className="truncate font-mono text-xs">{d.playbook_path}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{d.limit || '—'}</span>
            <DeleteTemplateDefinitionButton definition={d} />
          </GridRow>
        ))}
        {list.length === 0 && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.instance.shared.templates.empty}</p>
        )}
      </Card>
      {dialog === 'new' ? (
        <TemplateDefinitionDialog repoOptions={repoOptions} onClose={() => setDialog(null)} />
      ) : dialog ? (
        <TemplateDefinitionDialog definition={dialog} repoOptions={repoOptions} onClose={() => setDialog(null)} />
      ) : null}
    </>
  );
}

interface TemplateDefinitionFormValues {
  name: string;
  sharedRepoId: string;
  playbookPath: string;
  limit: string;
}

/** validateDefinitionInput's own doc comment, same reasoning, different
 * bounds — mirroring templateDefinitionBody's own minLength/maxLength
 * exactly (internal/api/shared_handlers.go), no format pattern beyond that. */
function validateTemplateDefinitionInput(
  values: TemplateDefinitionFormValues,
  t: Messages,
): Partial<Record<keyof TemplateDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof TemplateDefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = t.validation.required;
  else if (values.name.length > 63) errors.name = t.validation.maxChars(63);
  if (values.sharedRepoId.trim() === '') errors.sharedRepoId = t.validation.required;
  if (values.playbookPath.trim() === '') errors.playbookPath = t.validation.required;
  else if (values.playbookPath.length > 500) errors.playbookPath = t.validation.maxChars(500);
  if (values.limit.length > 500) errors.limit = t.validation.maxChars(500);
  return errors;
}

// SHARED_FIELD_TYPES is FIELD_TYPES without "secret": a shared
// definition's extra vars are written straight into every organization's
// own job_templates.extra_vars column, never sealed, so the server
// refuses a secret-typed field outright (internal/shared's
// schemaHasSecretField). Offering a type that is always refused is worse
// than not offering it (backlog 2.101).
const SHARED_FIELD_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum', 'list', 'repeater', 'object'];

/**
 * TemplateDefinitionDialog creates or edits one installation-owned job
 * template definition. No scope, no SSH credential, no vault identities —
 * decision 2 keeps every binding inside the organization that supplies it,
 * never on the definition. repoOptions is this dialog's one dependency on
 * the OTHER section (RepoDefinitionsSection) — a template always resolves
 * its playbook against a shared repository, never an organization's own
 * (shared_job_templates.shared_repo_id's own NOT NULL, migration 00061).
 */
function TemplateDefinitionDialog({
  definition, repoOptions, onClose,
}: {
  definition?: TemplateDefinition;
  repoOptions: { value: string; label: string }[];
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const labels: Record<Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>, string> = {
    name: t.instance.shared.name,
    playbookPath: t.instance.shared.templates.playbookPath,
    limit: t.instance.shared.templates.limitPattern,
  };
  const create = useCreateTemplateDefinition();
  const update = useUpdateTemplateDefinition(definition?.id ?? '');
  const [values, setValues] = useState<TemplateDefinitionFormValues>(
    definition
      ? { name: definition.name, sharedRepoId: definition.shared_repo_id, playbookPath: definition.playbook_path, limit: definition.limit }
      : { name: '', sharedRepoId: '', playbookPath: '', limit: '' },
  );
  const [errors, setErrors] = useState<Partial<Record<keyof TemplateDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [schema, setSchema] = useState<VarSchema>(
    definition?.extra_vars_schema ? toVarSchema(definition.extra_vars_schema) : { fields: [] },
  );
  const [vars, setVars] = useState<Vars>((definition?.extra_vars ?? {}) as Vars);

  function setValue(key: Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>, placeholder: string) {
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`tpl-def-${key}`}>{labels[key]}</Label>
        <Input
          id={`tpl-def-${key}`}
          className="font-mono"
          value={values[key]}
          onChange={setValue(key)}
          placeholder={placeholder}
          aria-invalid={errors[key] ? true : undefined}
          autoFocus={key === 'name'}
        />
        {errors[key] && (
          <span role="alert" className="text-[11px] text-destructive">
            {errors[key]}
          </span>
        )}
      </div>
    );
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError(null);
    const fieldErrors = validateTemplateDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    // Both are edited here since backlog 2.101; they used to round-trip
    // from the definition the dialog was opened with, because the form
    // did not render them and a PUT replaces everything. An empty field
    // list sends no schema at all rather than an empty one, so a
    // definition that never had one still round-trips as absent.
    const body: TemplateDefinitionInput = {
      name: values.name, shared_repo_id: values.sharedRepoId, playbook_path: values.playbookPath, limit: values.limit,
      extra_vars: vars,
      ...(schema.fields.length > 0 ? { extra_vars_schema: schema } : {}),
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.templates.saved(body.name) : t.instance.shared.templates.created(body.name),
      );
      onClose();
    } catch (err) {
      // RepoDefinitionDialog's own doc comment, same reasoning: decision 7's
      // and decision 5's messages are both shown whole here, not routed to
      // one field.
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition?.name ?? t.instance.shared.templates.newTitle}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {field('name', 'deploy-site')}
        <div className="flex flex-col gap-1">
          <Label htmlFor="tpl-def-shared-repo">{t.instance.shared.templates.sharedRepository}</Label>
          <Select
            id="tpl-def-shared-repo"
            invalid={!!errors.sharedRepoId}
            options={repoOptions}
            value={values.sharedRepoId}
            onChange={(v) => setValues((cur) => ({ ...cur, sharedRepoId: v }))}
            placeholder={t.instance.shared.templates.sharedRepositoryPlaceholder}
          />
          {errors.sharedRepoId && (
            <span role="alert" className="text-[11px] text-destructive">
              {errors.sharedRepoId}
            </span>
          )}
        </div>
        {field('playbookPath', 'site.yml')}
        {field('limit', t.instance.shared.templates.limitPlaceholder)}
        <details className="rounded-md border border-border p-3" open={schema.fields.length > 0}>
          <summary className="cursor-pointer text-xs font-semibold">{t.instance.shared.templates.extraVarsSchema}</summary>
          <div className="pt-3">
            {/* No secret type: the server refuses one on a shared
                definition by construction, because these extra vars are
                written into every organization's own column without ever
                being sealed. */}
            <SchemaBuilder nested types={SHARED_FIELD_TYPES} value={schema} onChange={setSchema} />
          </div>
        </details>
        {schema.fields.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-semibold">{t.instance.shared.templates.extraVars}</span>
            <SchemaForm fields={schema.fields} value={vars} onChange={setVars} idPrefix="tpl-def-vars" />
          </div>
        )}
        <p className="text-[11px] text-muted-foreground">{t.instance.shared.templates.hint}</p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// --- check definitions (Task 7) ---
//
// The third and last application of RepoDefinitionsSection's own shape: a
// check definition's shared half (spec decision 2) is key, plugin, params
// and interval — read-only inside every organization once projected
// (CheckDialog.tsx's own `shared` guard). UNLIKE the other two kinds, the
// binding (org/group/host) and whether it is enabled are NEVER on this
// dialog at all: decision 2 gives both to the projection, not the
// definition, for check definitions specifically — the organization fills
// them in itself through the ordinary Checks page, the same way it already
// does for a seeded row.
//
// Params editing is deliberately limited to the "service" plugin's units
// list here, the one parameter an instance admin is likely to set when
// defining a shared check — the same scope limitation
// TemplateDefinitionDialog draws around extra vars (a definition created
// without one still works; the server defaults params to {}), rather than
// re-hosting CheckDialog.tsx's own full per-plugin params editor
// (disk mounts included) for a dialog that otherwise has no per-organization
// state to show at all. That comment reasoned about CREATE; a review found
// the gap it did not cover — this is a full-replace PUT on EDIT too, and
// params() used to return {} for every plugin but "service" regardless, so
// saving a "disk" definition's interval (the one field this dialog renders
// for it) silently cleared its stored mounts allow-list for every
// organization's projection in the same transaction. params() below now
// rounds an unrendered plugin's stored params back through unchanged
// instead, the same fix TemplateDefinitionDialog's own body applies to
// extra vars.

/** DeleteDefinitionButton's own doc comment, same reasoning, different
 * table — except a check definition's own delete NEVER refuses with 409 at
 * all (spec decision 5 has nothing to block on: nothing in the schema
 * references check_definitions, internal/shared's own doc comment on
 * DeleteCheckDefinitionWithProjections), so the confirmation below says so
 * rather than warning about a dependant that can never exist. */
function DeleteCheckDefinitionButton({ definition }: { definition: CheckDefinitionDef }) {
  const t = useT();
  const toast = useToast();
  const remove = useDeleteCheckDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={t.instance.shared.deleteAria(definition.key)}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(t.instance.shared.checks.confirmDelete(definition.key))) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(t.instance.shared.checks.deleted(definition.key));
          } catch (err) {
            toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
          }
        }}
      >
        <Trash2 size={13} />
      </Button>
    </span>
  );
}

function CheckDefinitionsSection() {
  const t = useT();
  const definitions = useCheckDefinitions();
  const [dialog, setDialog] = useState<'new' | CheckDefinitionDef | null>(null);

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  return (
    <>
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {t.instance.shared.checks.count(list.length)}
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <Activity size={15} /> {t.instance.shared.checks.add}
        </Button>
      </div>
      <Card>
        <GridHeader columns={CHECK_COLUMNS}>
          <span>{t.instance.shared.checks.key}</span>
          <span>{t.instance.shared.checks.plugin}</span>
          <span>{t.instance.shared.checks.interval}</span>
          <span>{t.instance.shared.actions}</span>
        </GridHeader>
        {list.map((d) => (
          <GridRow key={d.id} columns={CHECK_COLUMNS}>
            <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(d)}>
              {d.key}
            </button>
            <span className="truncate text-xs">{d.plugin}</span>
            <span className="truncate text-xs text-muted-foreground">{d.interval_seconds}s</span>
            <DeleteCheckDefinitionButton definition={d} />
          </GridRow>
        ))}
        {list.length === 0 && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.instance.shared.checks.empty}</p>
        )}
      </Card>
      {dialog === 'new' ? (
        <CheckDefinitionDialog onClose={() => setDialog(null)} />
      ) : dialog ? (
        <CheckDefinitionDialog definition={dialog} onClose={() => setDialog(null)} />
      ) : null}
    </>
  );
}

interface CheckDefinitionFormValues {
  key: string;
  plugin: string;
  intervalSeconds: string;
  units: string;
  mounts: string;
}

const CHECK_PLUGINS = ['cpu', 'memory', 'disk', 'service'] as const;

/** lines turns a textarea's content into the string list the API takes —
 * CheckDialog.tsx's own helper, duplicated rather than imported: that
 * module's own export surface is the dialog component, not this one helper,
 * and the function is three lines. */
function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** validateDefinitionInput's own doc comment, same reasoning, different
 * bounds — mirroring checkDefinitionDefBody's own lack of format
 * constraints (internal/api/shared_handlers.go): checks.ValidateDefinitionShape
 * is the one validator every write funnels through, so this form only
 * refuses what would otherwise submit an empty, guaranteed-400 request. */
function validateCheckDefinitionInput(
  values: CheckDefinitionFormValues,
  t: Messages,
): Partial<Record<keyof CheckDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof CheckDefinitionFormValues, string>> = {};
  if (values.key.length > 63) errors.key = t.validation.maxChars(63);
  const interval = Number(values.intervalSeconds);
  if (!values.intervalSeconds || Number.isNaN(interval)) errors.intervalSeconds = t.validation.required;
  else if (interval < 10 || interval > 86400) errors.intervalSeconds = t.instance.shared.checks.intervalRange(10, 86400);
  if (values.plugin === 'service' && lines(values.units).length === 0) errors.units = t.validation.required;
  return errors;
}

/**
 * CheckDefinitionDialog creates or edits one installation-owned check
 * definition. No binding, no enabled flag — decision 2 keeps both on the
 * projection, which the organization edits through its own Checks page
 * (CheckDialog.tsx), never here.
 */
function CheckDefinitionDialog({ definition, onClose }: { definition?: CheckDefinitionDef; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const create = useCreateCheckDefinition();
  const update = useUpdateCheckDefinition(definition?.id ?? '');
  const [values, setValues] = useState<CheckDefinitionFormValues>(
    definition
      ? {
          key: definition.key, plugin: definition.plugin, intervalSeconds: String(definition.interval_seconds),
          units: Array.isArray(definition.params?.units) ? (definition.params.units as string[]).join('\n') : '',
          mounts: Array.isArray(definition.params?.mounts) ? (definition.params.mounts as string[]).join('\n') : '',
        }
      : { key: '', plugin: 'cpu', intervalSeconds: '60', units: '', mounts: '' },
  );
  const [errors, setErrors] = useState<Partial<Record<keyof CheckDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function params(): Record<string, unknown> {
    if (values.plugin === 'service') return { units: lines(values.units) };
    // disk's allow-list is optional and an empty one means every mount
    // (CheckDialog.tsx keeps the same contract for an organization's own
    // checks), so an empty textarea sends {} rather than an empty list.
    if (values.plugin === 'disk') {
      const list = lines(values.mounts);
      return list.length > 0 ? { mounts: list } : {};
    }
    // cpu and memory take no params at all. Any plugin added later is
    // still unrendered here, so its stored value round-trips unchanged
    // while the plugin itself is unchanged — switching plugins away from
    // what params() was written against would carry over a shape the new
    // plugin's own validation refuses anyway, so {} is right there.
    if (definition && definition.plugin === values.plugin) return definition.params ?? {};
    return {};
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError(null);
    const fieldErrors = validateCheckDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    const body: CheckDefinitionDefInput = {
      ...(values.key !== '' ? { key: values.key } : {}),
      plugin: values.plugin, interval_seconds: Number(values.intervalSeconds), params: params(),
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.checks.saved(body.plugin) : t.instance.shared.checks.created(body.plugin),
      );
      onClose();
    } catch (err) {
      // RepoDefinitionDialog's own doc comment, same reasoning: decision 7's
      // collision message names every colliding organization, shown whole
      // rather than routed to one field.
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition ? definition.key : t.instance.shared.checks.newTitle}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-plugin">{t.instance.shared.checks.plugin}</Label>
            <Select
              id="check-def-plugin"
              options={CHECK_PLUGINS.map((value) => ({ value, label: t.instance.shared.checks.plugins[value] }))}
              value={values.plugin}
              onChange={(v) => setValues((cur) => ({ ...cur, plugin: v }))}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-key">{t.instance.shared.checks.key}</Label>
            <Input
              id="check-def-key"
              className="font-mono"
              value={values.key}
              placeholder={values.plugin}
              onChange={(e) => setValues((cur) => ({ ...cur, key: e.target.value.trim() }))}
              aria-invalid={errors.key ? true : undefined}
            />
            {errors.key && (
              <span role="alert" className="text-[11px] text-destructive">
                {errors.key}
              </span>
            )}
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="check-def-interval">{t.instance.shared.checks.intervalSeconds}</Label>
          <Input
            id="check-def-interval"
            type="number"
            min={10}
            max={86400}
            value={values.intervalSeconds}
            onChange={(e) => setValues((cur) => ({ ...cur, intervalSeconds: e.target.value }))}
            aria-invalid={errors.intervalSeconds ? true : undefined}
          />
          {errors.intervalSeconds && (
            <span role="alert" className="text-[11px] text-destructive">
              {errors.intervalSeconds}
            </span>
          )}
        </div>
        {values.plugin === 'disk' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-mounts">{t.instance.shared.checks.mounts}</Label>
            <textarea
              id="check-def-mounts"
              className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
              value={values.mounts}
              placeholder="/&#10;/var"
              onChange={(e) => setValues((cur) => ({ ...cur, mounts: e.target.value }))}
            />
          </div>
        )}
        {values.plugin === 'service' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-units">{t.instance.shared.checks.units}</Label>
            <textarea
              id="check-def-units"
              className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
              value={values.units}
              placeholder="sshd"
              onChange={(e) => setValues((cur) => ({ ...cur, units: e.target.value }))}
            />
            {errors.units && (
              <span role="alert" className="text-[11px] text-destructive">
                {errors.units}
              </span>
            )}
          </div>
        )}
        <p className="text-[11px] text-muted-foreground">{t.instance.shared.checks.hint}</p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
