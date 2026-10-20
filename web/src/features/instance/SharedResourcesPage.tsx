import { useState, type ChangeEvent, type FormEvent } from 'react';
import { useParams } from '@tanstack/react-router';
import { GitBranch, Trash2 } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
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
import type { RepoDefinition, RepoDefinitionInput } from '../../api/types.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 1.3fr) 110px minmax(0, 0.9fr) 90px';

/**
 * SharedResourcesPage is where an instance admin defines a playbook
 * repository once, for every organization (spec v0.2f, decisions 1-8):
 * name, url, branch and subpath live here and are projected into every
 * organization as an ordinary, read-only-in-those-four-fields repository
 * (RepoDialog.tsx) — each organization still supplies its own credential,
 * since a credential never belongs to a definition (decision 3).
 *
 * Reachable with Principal.InstanceAdmin only, the same gate
 * SmtpSettingsPage's /admin/settings/smtp already uses: GET/POST/PUT/DELETE
 * /admin/repos/definitions admit no other caller, so a non-admin reaching
 * this page (its tab is hidden from them, but the route itself is not
 * guarded client-side) sees the 403 QueryError page instead of the list.
 * It lives under an organization's Settings only for navigation, exactly
 * as SmtpSettingsPage's own doc comment says of itself — the definitions
 * are instance-wide, not this organization's.
 */
export function SharedResourcesPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const definitions = useRepoDefinitions();
  const [dialog, setDialog] = useState<'new' | RepoDefinition | null>(null);

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">Shared resources</h1>
        <SettingsTabs org={org} />
      </div>
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {list.length} repository definitions · projected into every organization, read-only there
        </span>
        <Button variant="primary" onClick={() => setDialog('new')}>
          <GitBranch size={15} /> Add repository definition
        </Button>
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Name</span>
          <span>URL</span>
          <span>Branch</span>
          <span>Subpath</span>
          <span>Actions</span>
        </GridHeader>
        {list.map((d) => (
          <GridRow key={d.id} columns={COLUMNS}>
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
