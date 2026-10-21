import { useState, type ChangeEvent, type FormEvent } from 'react';
import { useParams } from '@tanstack/react-router';
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
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
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
  const { org } = useParams({ strict: false }) as { org: string };
  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">Shared resources</h1>
        <SettingsTabs org={org} />
      </div>
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
          {list.length} repository definitions · projected into every organization, read-only there
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <GitBranch size={15} /> Add repository definition
        </Button>
      </div>
      <Card>
        <GridHeader columns={REPO_COLUMNS}>
          <span>Name</span>
          <span>URL</span>
          <span>Branch</span>
          <span>Subpath</span>
          <span>Actions</span>
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
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">No shared repository definitions yet.</p>
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
  const toast = useToast();
  const remove = useDeleteRepoDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={`Delete ${definition.name}`}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(`Delete repository definition ${definition.name}? This removes it from every organization.`)) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(`Repository definition ${definition.name} deleted`);
          } catch (err) {
            toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
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

const FIELD_LABEL: Record<keyof DefinitionFormValues, string> = {
  name: 'Name',
  url: 'URL',
  branch: 'Branch',
  subpath: 'Subdirectory',
};

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
function validateDefinitionInput(values: DefinitionFormValues): Partial<Record<keyof DefinitionFormValues, string>> {
  const errors: Partial<Record<keyof DefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = 'required';
  else if (values.name.length > 63) errors.name = 'must be 63 characters or fewer';
  if (values.url.trim() === '') errors.url = 'required';
  else if (values.url.length > 500) errors.url = 'must be 500 characters or fewer';
  if (values.branch.length > 100) errors.branch = 'must be 100 characters or fewer';
  if (values.subpath.length > 200) errors.subpath = 'must be 200 characters or fewer';
  return errors;
}

/**
 * RepoDefinitionDialog creates or edits one installation-owned repository
 * definition. No credential field — decision 3 keeps every credential
 * inside the organization that supplies it, never on the definition a
 * worker in any organization's zone could reach.
 */
function RepoDefinitionDialog({ definition, onClose }: { definition?: RepoDefinition; onClose: () => void }) {
  const toast = useToast();
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
        <Label htmlFor={`repo-def-${key}`}>{FIELD_LABEL[key]}</Label>
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
    const fieldErrors = validateDefinitionInput(values);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    const body: RepoDefinitionInput = { name: values.name, url: values.url, branch: values.branch, subpath: values.subpath };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(definition ? `Repository definition ${body.name} saved` : `Repository definition ${body.name} created`);
      onClose();
    } catch (err) {
      // Shown whole, not routed to one field: decision 7's collision
      // message names every colliding organization and decision 5's
      // in-use message names one plus what still references it — neither
      // is about a single field the way features/repos/validate.ts's own
      // server-mirror errors are.
      setFormError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition?.name ?? 'New repository definition'}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {field('name', 'ansible-base')}
        {field('url', 'https://git.example/provider/ansible-base.git')}
        {field('branch', 'main')}
        {field('subpath', 'ansible/ (optional)')}
        <p className="text-[11px] text-muted-foreground">
          Projected into every organization as a read-only repository; each one supplies its own credential here, not
          on this definition.
        </p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? 'Save definition' : 'Create definition'}
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
// — the server defaults extra_vars to {} and the schema to absent either way.

/** DeleteDefinitionButton's own doc comment, same reasoning, different
 * table: a schedule, trigger or (for a template) nothing-else-new blocks
 * with 409 naming the organization — DeleteTemplateWithProjections' own
 * inventory (internal/shared/store_sql.go) is schedules and job triggers
 * only; job_template_vault_credentials cascades on purpose and never
 * appears in this message. */
function DeleteTemplateDefinitionButton({ definition }: { definition: TemplateDefinition }) {
  const toast = useToast();
  const remove = useDeleteTemplateDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={`Delete ${definition.name}`}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(`Delete job template definition ${definition.name}? This removes it from every organization.`)) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(`Job template definition ${definition.name} deleted`);
          } catch (err) {
            toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
          }
        }}
      >
        <Trash2 size={13} />
      </Button>
    </span>
  );
}

function TemplateDefinitionsSection() {
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
          {list.length} job template definitions · projected into every organization, read-only there
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <ListPlus size={15} /> Add job template definition
        </Button>
      </div>
      <Card>
        <GridHeader columns={TEMPLATE_COLUMNS}>
          <span>Name</span>
          <span>Repository</span>
          <span>Playbook path</span>
          <span>Limit</span>
          <span>Actions</span>
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
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">No shared job template definitions yet.</p>
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

const TEMPLATE_FIELD_LABEL: Record<Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>, string> = {
  name: 'Name',
  playbookPath: 'Playbook path',
  limit: 'Limit pattern',
};

/** validateDefinitionInput's own doc comment, same reasoning, different
 * bounds — mirroring templateDefinitionBody's own minLength/maxLength
 * exactly (internal/api/shared_handlers.go), no format pattern beyond that. */
function validateTemplateDefinitionInput(values: TemplateDefinitionFormValues): Partial<Record<keyof TemplateDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof TemplateDefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = 'required';
  else if (values.name.length > 63) errors.name = 'must be 63 characters or fewer';
  if (values.sharedRepoId.trim() === '') errors.sharedRepoId = 'required';
  if (values.playbookPath.trim() === '') errors.playbookPath = 'required';
  else if (values.playbookPath.length > 500) errors.playbookPath = 'must be 500 characters or fewer';
  if (values.limit.length > 500) errors.limit = 'must be 500 characters or fewer';
  return errors;
}

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
  const toast = useToast();
  const create = useCreateTemplateDefinition();
  const update = useUpdateTemplateDefinition(definition?.id ?? '');
  const [values, setValues] = useState<TemplateDefinitionFormValues>(
    definition
      ? { name: definition.name, sharedRepoId: definition.shared_repo_id, playbookPath: definition.playbook_path, limit: definition.limit }
      : { name: '', sharedRepoId: '', playbookPath: '', limit: '' },
  );
  const [errors, setErrors] = useState<Partial<Record<keyof TemplateDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function setValue(key: Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>, placeholder: string) {
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`tpl-def-${key}`}>{TEMPLATE_FIELD_LABEL[key]}</Label>
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
    const fieldErrors = validateTemplateDefinitionInput(values);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    const body: TemplateDefinitionInput = {
      name: values.name, shared_repo_id: values.sharedRepoId, playbook_path: values.playbookPath, limit: values.limit,
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(definition ? `Job template definition ${body.name} saved` : `Job template definition ${body.name} created`);
      onClose();
    } catch (err) {
      // RepoDefinitionDialog's own doc comment, same reasoning: decision 7's
      // and decision 5's messages are both shown whole here, not routed to
      // one field.
      setFormError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition?.name ?? 'New job template definition'}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {field('name', 'deploy-site')}
        <div className="flex flex-col gap-1">
          <Label htmlFor="tpl-def-shared-repo">Shared repository</Label>
          <Select
            id="tpl-def-shared-repo"
            invalid={!!errors.sharedRepoId}
            options={repoOptions}
            value={values.sharedRepoId}
            onChange={(v) => setValues((cur) => ({ ...cur, sharedRepoId: v }))}
            placeholder="Choose a shared repository…"
          />
          {errors.sharedRepoId && (
            <span role="alert" className="text-[11px] text-destructive">
              {errors.sharedRepoId}
            </span>
          )}
        </div>
        {field('playbookPath', 'site.yml')}
        {field('limit', 'e.g. web*:&!web-03 (optional)')}
        <p className="text-[11px] text-muted-foreground">
          Projected into every organization as a read-only template; each one supplies its own scope and SSH
          credential here, not on this definition. Extra vars and their schema are set through the API for now.
        </p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? 'Save definition' : 'Create definition'}
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
// state to show at all.

/** DeleteDefinitionButton's own doc comment, same reasoning, different
 * table — except a check definition's own delete NEVER refuses with 409 at
 * all (spec decision 5 has nothing to block on: nothing in the schema
 * references check_definitions, internal/shared's own doc comment on
 * DeleteCheckDefinitionWithProjections), so the confirmation below says so
 * rather than warning about a dependant that can never exist. */
function DeleteCheckDefinitionButton({ definition }: { definition: CheckDefinitionDef }) {
  const toast = useToast();
  const remove = useDeleteCheckDefinition();
  return (
    <span className="flex justify-end">
      <Button
        type="button"
        variant="ghost"
        aria-label={`Delete ${definition.key}`}
        disabled={remove.isPending}
        onClick={async () => {
          if (!confirm(`Delete check definition ${definition.key}? This removes it from every organization.`)) return;
          try {
            await remove.mutateAsync(definition.id);
            toast.show(`Check definition ${definition.key} deleted`);
          } catch (err) {
            toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
          }
        }}
      >
        <Trash2 size={13} />
      </Button>
    </span>
  );
}

function CheckDefinitionsSection() {
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
          {list.length} check definitions · projected into every organization at the default org-wide binding, read-only there
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <Activity size={15} /> Add check definition
        </Button>
      </div>
      <Card>
        <GridHeader columns={CHECK_COLUMNS}>
          <span>Key</span>
          <span>Plugin</span>
          <span>Interval</span>
          <span>Actions</span>
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
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">No shared check definitions yet.</p>
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
}

const CHECK_PLUGINS: [string, string][] = [
  ['cpu', 'CPU'],
  ['memory', 'Memory'],
  ['disk', 'Disk'],
  ['service', 'Service units'],
];

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
function validateCheckDefinitionInput(values: CheckDefinitionFormValues): Partial<Record<keyof CheckDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof CheckDefinitionFormValues, string>> = {};
  if (values.key.length > 63) errors.key = 'must be 63 characters or fewer';
  const interval = Number(values.intervalSeconds);
  if (!values.intervalSeconds || Number.isNaN(interval)) errors.intervalSeconds = 'required';
  else if (interval < 10 || interval > 86400) errors.intervalSeconds = 'must be between 10 and 86400';
  if (values.plugin === 'service' && lines(values.units).length === 0) errors.units = 'required';
  return errors;
}

/**
 * CheckDefinitionDialog creates or edits one installation-owned check
 * definition. No binding, no enabled flag — decision 2 keeps both on the
 * projection, which the organization edits through its own Checks page
 * (CheckDialog.tsx), never here.
 */
function CheckDefinitionDialog({ definition, onClose }: { definition?: CheckDefinitionDef; onClose: () => void }) {
  const toast = useToast();
  const create = useCreateCheckDefinition();
  const update = useUpdateCheckDefinition(definition?.id ?? '');
  const [values, setValues] = useState<CheckDefinitionFormValues>(
    definition
      ? {
          key: definition.key, plugin: definition.plugin, intervalSeconds: String(definition.interval_seconds),
          units: Array.isArray(definition.params?.units) ? (definition.params.units as string[]).join('\n') : '',
        }
      : { key: '', plugin: 'cpu', intervalSeconds: '60', units: '' },
  );
  const [errors, setErrors] = useState<Partial<Record<keyof CheckDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function params(): Record<string, unknown> {
    return values.plugin === 'service' ? { units: lines(values.units) } : {};
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError(null);
    const fieldErrors = validateCheckDefinitionInput(values);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return;
    const body: CheckDefinitionDefInput = {
      ...(values.key !== '' ? { key: values.key } : {}),
      plugin: values.plugin, interval_seconds: Number(values.intervalSeconds), params: params(),
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(definition ? `Check definition ${body.plugin} saved` : `Check definition ${body.plugin} created`);
      onClose();
    } catch (err) {
      // RepoDefinitionDialog's own doc comment, same reasoning: decision 7's
      // collision message names every colliding organization, shown whole
      // rather than routed to one field.
      setFormError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={definition ? definition.key : 'New check definition'}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-plugin">Plugin</Label>
            <Select
              id="check-def-plugin"
              options={CHECK_PLUGINS.map(([value, label]) => ({ value, label }))}
              value={values.plugin}
              onChange={(v) => setValues((cur) => ({ ...cur, plugin: v }))}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-key">Key</Label>
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
          <Label htmlFor="check-def-interval">Interval (seconds)</Label>
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
        {values.plugin === 'service' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-units">Units (one per line)</Label>
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
        <p className="text-[11px] text-muted-foreground">
          Projected into every organization at the default org-wide binding, enabled — each one may rebind it to a
          monitoring group or a host, or disable it, from its own Checks page; neither is set here.
        </p>
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {definition ? 'Save definition' : 'Create definition'}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
