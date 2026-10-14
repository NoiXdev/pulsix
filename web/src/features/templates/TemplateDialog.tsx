import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useCreateTemplate, useTemplate, useUpdateTemplate } from '../../api/hooks/useTemplates.ts';
import { SchemaForm } from '../inventory/schema/SchemaForm.tsx';
import { SchemaBuilder } from '../inventory/schema/SchemaBuilder.tsx';
import { problemsFromError } from '../inventory/schema/paths.ts';
import type { Problem, VarSchema, Vars } from '../inventory/schema/types.ts';
import { toVarSchema } from '../../api/hooks/useInventory.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { KIND_LABEL } from '../credentials/labels.ts';
import { ScopePicker, type Scope } from './ScopePicker.tsx';
import { toEditableVars, varsForPut } from './vars.ts';

export interface TemplateDialogProps {
  org: string;
  /** The template to edit; absent means create. */
  template?: { id: string; name: string };
  canReadCredentials: boolean;
  onClose: () => void;
}

/**
 * TemplateDialog creates or edits a job template: name, playbook, scope
 * (the inventory scope picker), a free-form limit, the optional typed
 * extra-vars schema with the values form built from it, and the default
 * SSH credential. Secret-typed extra vars follow Task 7's roundtrip
 * contract: a value the server has ("value set on server, not shown") is
 * display-only and rides the PUT as a keep marker (the server keeps the
 * sealed value); replacing it sends the new plaintext (templates/vars.ts).
 */
export function TemplateDialog({ org, template, canReadCredentials, onClose }: TemplateDialogProps) {
  const detail = useTemplate(org, template?.id ?? '');
  const playbooks = usePlaybooks(org);
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const guard = useQueryGuard(detail);

  if (template) {
    if (guard) {
      return (
        <Dialog open onClose={onClose} title={template.name}>
          {guard}
        </Dialog>
      );
    }
    if (!detail.data?.template) {
      return (
        <Dialog open onClose={onClose} title={template.name}>
          <QueryLoading />
        </Dialog>
      );
    }
    const t = detail.data.template;
    return (
      <TemplateForm
        org={org}
        existing={{
          id: t.id,
          name: t.name,
          playbookId: t.playbook_id,
          scope: { group_id: t.scope.group_id ?? '', host_ids: t.scope.host_ids ?? [] },
          limit: t.limit,
          schema: t.extra_vars_schema ? toVarSchema(t.extra_vars_schema) : null,
          vars: t.extra_vars ?? {},
          credentialId: t.credential_id ?? '',
        }}
        playbooks={playbooks.data?.playbooks ?? []}
        credentials={(credentials.data?.credentials ?? []).filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
        onClose={onClose}
      />
    );
  }
  return (
    <TemplateForm
      org={org}
      playbooks={playbooks.data?.playbooks ?? []}
      credentials={(credentials.data?.credentials ?? []).filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
      onClose={onClose}
    />
  );
}

interface Existing {
  id: string;
  name: string;
  playbookId: string;
  scope: Scope;
  limit: string;
  schema: VarSchema | null;
  vars: Vars;
  credentialId: string;
}

function TemplateForm({
  org, existing, playbooks, credentials, onClose,
}: {
  org: string;
  existing?: Existing;
  playbooks: readonly { id: string; source: string; summary: string; path?: string }[];
  credentials: readonly { id: string; name: string; kind: string }[];
  onClose: () => void;
}) {
  const toast = useToast();
  const create = useCreateTemplate(org);
  const update = useUpdateTemplate(org, existing?.id ?? '');

  const [name, setName] = useState(existing?.name ?? '');
  const [playbookId, setPlaybookId] = useState(existing?.playbookId ?? '');
  const [scope, setScope] = useState<Scope>(existing?.scope ?? { group_id: '', host_ids: [] });
  const [limit, setLimit] = useState(existing?.limit ?? '');
  const [schema, setSchema] = useState<VarSchema | null>(existing?.schema ?? null);
  // The extra-vars form follows the LIVE schema state: a schema built in
  // this dialog — a create, or an edit that adds one — must produce its
  // fields immediately, not only after the template round-trips (the
  // stored-schema derivation left the form hidden and a strict-validation
  // 422 nowhere to be shown).
  const fields = schema?.fields ?? [];
  // With a schema the loaded vars go through the keep-marker conversion
  // (declared secret fields display-only); without one they pass through
  // unchanged — varsForPut strips any set-marker either way.
  const [vars, setVars] = useState<Vars>(() => {
    const loaded = existing?.schema?.fields ?? [];
    return existing ? (loaded.length > 0 ? toEditableVars(loaded, existing.vars) : existing.vars) : {};
  });
  const [credentialId, setCredentialId] = useState(existing?.credentialId ?? '');
  // null = closed; a string is the name typed into the picker before it
  // opened this.
  const [newCredentialName, setNewCredentialName] = useState<string | null>(null);
  const [varProblems, setVarProblems] = useState<Problem[]>([]);
  const [schemaProblems, setSchemaProblems] = useState<Problem[]>([]);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setVarProblems([]);
    setSchemaProblems([]);
    const body = {
      name,
      playbook_id: playbookId,
      scope: scope.group_id || scope.host_ids.length > 0 ? { group_id: scope.group_id || undefined, host_ids: scope.host_ids.length > 0 ? scope.host_ids : undefined } : undefined,
      limit: limit || undefined,
      extra_vars: varsForPut(schema?.fields, vars),
      extra_vars_schema: schema && schema.fields.length > 0 ? schema : undefined,
      credential_id: credentialId || undefined,
    };
    try {
      if (existing) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(existing ? `Template ${body.name} saved` : `Template ${body.name} created`);
      onClose();
    } catch (err) {
      const varPs = problemsFromError(err, 'body.extra_vars');
      if (varPs.length > 0) return setVarProblems(varPs);
      const schemaPs = problemsFromError(err, 'body.extra_vars_schema');
      if (schemaPs.length > 0) return setSchemaProblems(schemaPs);
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={existing?.name ?? 'New job template'}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-name">Name</Label>
            <Input id="template-name" className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="deploy-site" autoFocus={!existing} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-playbook">Playbook</Label>
            <Select
              id="template-playbook"
              required
              options={playbooks.map((p) => ({ value: p.id, label: p.source === 'repo' ? `repo: ${p.path ?? ''}` : p.summary || 'editor playbook' }))}
              value={playbookId}
              onChange={setPlaybookId}
              placeholder="Choose a playbook…"
            />
          </div>
        </div>
        <ScopePicker org={org} value={scope} onChange={setScope} />
        <div className="flex flex-col gap-1">
          <Label htmlFor="template-limit">Limit pattern</Label>
          <Input id="template-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder="e.g. web*:&!web-03" />
          <span className="text-[11px] text-muted-foreground">Free-form ansible limit pattern, empty = no limit.</span>
        </div>
        <details className="rounded-md border border-border p-3" open={!!existing?.schema}>
          <summary className="cursor-pointer text-xs font-semibold">Extra vars schema</summary>
          <div className="pt-3">
            <SchemaBuilder value={schema ?? { fields: [] }} onChange={setSchema} problems={schemaProblems} nested />
          </div>
        </details>
        {fields.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-semibold">Extra vars</span>
            <SchemaForm fields={fields} value={vars} onChange={setVars} problems={varProblems} idPrefix="template-vars" />
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="template-credential">Default SSH credential</Label>
          <Select
            id="template-credential"
            // This list mixes SSH keys and SSH passwords (see the filter in
            // TemplateDialog above), and nothing stops two of either kind
            // sharing a name.
            options={credentials.map((c) => ({ value: c.id, label: c.name, hint: KIND_LABEL[c.kind] ?? c.kind }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder="— none —"
            create={{ label: 'New credential', onSelect: setNewCredentialName }}
          />
          <span className="text-[11px] text-muted-foreground">SSH key or password credential for hosts without one of their own.</span>
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!playbookId || create.isPending || update.isPending}>
            {existing ? 'Save template' : 'Create template'}
          </Button>
        </div>
      </form>
      {/* Sibling of the <form> above, not inside it — see HostSshCard.tsx's
          own comment on why a nested <dialog> here would double-submit. */}
      {newCredentialName !== null && (
        <AddCredentialDialog
          org={org}
          open
          initialName={newCredentialName}
          onClose={() => setNewCredentialName(null)}
          onCreated={setCredentialId}
        />
      )}
    </Dialog>
  );
}
