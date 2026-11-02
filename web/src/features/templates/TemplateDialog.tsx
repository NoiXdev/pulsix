import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useCreateTemplate, useDeleteTemplate, useTemplate, useUpdateTemplate } from '../../api/hooks/useTemplates.ts';
import { SchemaForm } from '../inventory/schema/SchemaForm.tsx';
import { SchemaBuilder } from '../inventory/schema/SchemaBuilder.tsx';
import { problemsFromError } from '../inventory/schema/paths.ts';
import type { Problem, VarSchema, Vars } from '../inventory/schema/types.ts';
import { toVarSchema } from '../../api/hooks/useInventory.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { kindName } from '../credentials/labels.ts';
import { useT } from '../../i18n/useT.ts';
import { VaultIdentitiesField } from '../credentials/VaultIdentitiesField.tsx';
import type { Credential } from '../../api/types.ts';
import { identitiesForBody, rowsFromIdentities, vaultIdentityErrors, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { ScopePicker, type Scope } from './ScopePicker.tsx';
import { toEditableVars, varsForPut } from './vars.ts';

export interface TemplateDialogProps {
  org: string;
  /** The template to edit; absent means create. */
  template?: { id: string; name: string };
  canReadCredentials: boolean;
  /** Gates the Default SSH credential picker's own create row:
   * credentials.write is admin, stricter than templates.write (v0.4g
   * Task 7 fix round). */
  canCreateCredential: boolean;
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
export function TemplateDialog({ org, template, canReadCredentials, canCreateCredential, onClose }: TemplateDialogProps) {
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
    const tpl = detail.data.template;
    const allCredentials = credentials.data?.credentials ?? [];
    return (
      <TemplateForm
        org={org}
        existing={{
          id: tpl.id,
          name: tpl.name,
          playbookId: tpl.playbook_id,
          scope: { group_id: tpl.scope.group_id ?? '', host_ids: tpl.scope.host_ids ?? [] },
          limit: tpl.limit,
          schema: tpl.extra_vars_schema ? toVarSchema(tpl.extra_vars_schema) : null,
          vars: tpl.extra_vars ?? {},
          credentialId: tpl.credential_id ?? '',
          vaultIdentities: rowsFromIdentities(tpl.vault_identities),
          sharedTemplateId: tpl.shared_template_id,
        }}
        playbooks={playbooks.data?.playbooks ?? []}
        credentials={allCredentials.filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
        vaultCredentials={allCredentials}
        canCreateCredential={canCreateCredential}
        onClose={onClose}
      />
    );
  }
  const allCredentials = credentials.data?.credentials ?? [];
  return (
    <TemplateForm
      org={org}
      playbooks={playbooks.data?.playbooks ?? []}
      credentials={allCredentials.filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
      vaultCredentials={allCredentials}
      canCreateCredential={canCreateCredential}
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
  vaultIdentities: VaultIdentityRow[];
  /** Set when this template is a projection of an installation-owned
   * definition (spec v0.2f decisions 1, 2). Its presence, not its value, is
   * what the form acts on: name, playbook, limit and the extra-vars schema
   * (and the vars themselves) become read-only — templates.Service already
   * refuses to change them on a projection (refuseSharedFieldChange,
   * internal/templates/service.go) regardless of what this dialog sends,
   * the same reasoning RepoDialog's own `projected` flag documents. Scope,
   * the default SSH credential and the vault identity set stay editable:
   * they are the projection's own. */
  sharedTemplateId?: string;
}

function TemplateForm({
  org, existing, playbooks, credentials, vaultCredentials, canCreateCredential, onClose,
}: {
  org: string;
  existing?: Existing;
  playbooks: readonly { id: string; source: string; summary: string; path?: string }[];
  credentials: readonly { id: string; name: string; kind: string }[];
  /** The org's full credential list, unfiltered — `credentials` above is
   * already narrowed to ssh_key/ssh_password for the Default SSH
   * credential field, which excludes every vault_password credential the
   * Vault identities list below needs to offer. */
  vaultCredentials: readonly Credential[];
  canCreateCredential: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const create = useCreateTemplate(org);
  const update = useUpdateTemplate(org, existing?.id ?? '');
  const remove = useDeleteTemplate(org);
  // Set means this row is a projection of an installation-owned definition
  // (spec v0.2f decision 1): name, playbook, limit, extra vars and their
  // schema are the definition's own, refused on write by templates.Service.
  // Update (refuseSharedFieldChange, internal/templates/service.go)
  // regardless of what this dialog sends, so they are disabled rather than
  // merely validated client-side — RepoDialog's own `projected` flag, same
  // reasoning.
  const projected = Boolean(existing?.sharedTemplateId);

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
  const [vaultRows, setVaultRows] = useState<VaultIdentityRow[]>(existing?.vaultIdentities ?? []);
  const [vaultErrors, setVaultErrors] = useState<Record<string, string>>({});
  // null = closed. One mount point for the Default SSH credential field and
  // every row of the Vault identities list, since only one AddCredentialDialog
  // can be open at a time (the same division RepoDialog's own
  // `addingCredential` keeps).
  const [addingCredential, setAddingCredential] = useState<{ field: 'ssh'; name: string } | { field: 'vault-identity'; rowKey: string; name: string } | null>(null);
  const [varProblems, setVarProblems] = useState<Problem[]>([]);
  const [schemaProblems, setSchemaProblems] = useState<Problem[]>([]);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setVarProblems([]);
    setSchemaProblems([]);
    const identityErrors = vaultIdentityErrors(vaultRows, t);
    setVaultErrors(identityErrors);
    if (Object.keys(identityErrors).length > 0) return;
    const body = {
      name,
      playbook_id: playbookId,
      scope: scope.group_id || scope.host_ids.length > 0 ? { group_id: scope.group_id || undefined, host_ids: scope.host_ids.length > 0 ? scope.host_ids : undefined } : undefined,
      limit: limit || undefined,
      extra_vars: varsForPut(schema?.fields, vars),
      extra_vars_schema: schema && schema.fields.length > 0 ? schema : undefined,
      credential_id: credentialId || undefined,
      vault_identities: identitiesForBody(vaultRows),
    };
    try {
      if (existing) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(existing ? t.templates.dialog.saved(body.name) : t.templates.dialog.created(body.name));
      onClose();
    } catch (err) {
      const varPs = problemsFromError(err, 'body.extra_vars');
      if (varPs.length > 0) return setVarProblems(varPs);
      const schemaPs = problemsFromError(err, 'body.extra_vars_schema');
      if (schemaPs.length > 0) return setSchemaProblems(schemaPs);
      setError(err instanceof Error ? err.message : t.templates.dialog.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={existing?.name ?? t.templates.dialog.newTitle}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        {projected && (
          <div className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5">
            <Pill tone="accent">{t.templates.list.managed}</Pill>
            <span className="text-[11px] text-muted-foreground">
              {t.templates.dialog.managedNote}
            </span>
          </div>
        )}
        <fieldset disabled={projected} className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-name">{t.templates.dialog.name}</Label>
            <Input id="template-name" className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder={t.templates.dialog.namePlaceholder} autoFocus={!existing} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-playbook">{t.templates.dialog.playbook}</Label>
            <Select
              id="template-playbook"
              required
              disabled={projected}
              options={playbooks.map((p) => ({ value: p.id, label: p.source === 'repo' ? t.templates.playbook.repo(p.path ?? '') : p.summary || t.templates.playbook.editor }))}
              value={playbookId}
              onChange={setPlaybookId}
              placeholder={t.templates.dialog.choosePlaybook}
            />
          </div>
        </fieldset>
        <ScopePicker org={org} value={scope} onChange={setScope} />
        <fieldset disabled={projected} className="flex flex-col gap-1">
          <Label htmlFor="template-limit">{t.templates.dialog.limit}</Label>
          <Input id="template-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder={t.templates.dialog.limitPlaceholder} />
          <span className="text-[11px] text-muted-foreground">{t.templates.dialog.limitHint}</span>
        </fieldset>
        <fieldset disabled={projected}>
          <details className="rounded-md border border-border p-3" open={!!existing?.schema}>
            <summary className="cursor-pointer text-xs font-semibold">{t.templates.dialog.schemaSummary}</summary>
            <div className="pt-3">
              <SchemaBuilder value={schema ?? { fields: [] }} onChange={setSchema} problems={schemaProblems} nested />
            </div>
          </details>
        </fieldset>
        {fields.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-semibold">{t.templates.dialog.extraVars}</span>
            <SchemaForm fields={fields} value={vars} onChange={setVars} problems={varProblems} idPrefix="template-vars" disabled={projected} />
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="template-credential">{t.templates.dialog.credential}</Label>
          <Select
            id="template-credential"
            // This list mixes SSH keys and SSH passwords (see the filter in
            // TemplateDialog above), and nothing stops two of either kind
            // sharing a name.
            options={credentials.map((c) => ({ value: c.id, label: c.name, hint: kindName(c.kind, t) }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder={t.templates.dialog.credentialNone}
            create={canCreateCredential ? { label: t.templates.dialog.newCredential, onSelect: (text) => setAddingCredential({ field: 'ssh', name: text }) } : undefined}
          />
          <span className="text-[11px] text-muted-foreground">{t.templates.dialog.credentialHint}</span>
        </div>
        <VaultIdentitiesField
          rows={vaultRows}
          onChange={setVaultRows}
          credentials={vaultCredentials}
          canCreateCredential={canCreateCredential}
          errors={vaultErrors}
          onRequestCreate={(row, text) => setAddingCredential({ field: 'vault-identity', rowKey: row.key, name: text })}
          help={t.templates.dialog.vaultHelp}
        />
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex items-center justify-between gap-2">
          {existing && !projected && (
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={async () => {
                if (!confirm(t.templates.dialog.confirmDelete(existing.name))) return;
                try {
                  await remove.mutateAsync(existing.id);
                  toast.show(t.templates.dialog.deleted(existing.name));
                  onClose();
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : t.templates.dialog.deleteFailed, 'destructive');
                }
              }}
            >
              {t.common.delete}
            </Button>
          )}
          <div className="flex gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button
              type="submit"
              variant="primary"
              // An ordinary template genuinely cannot be created or saved
              // without a playbook — the Select above is `required` for
              // exactly that case, and this guard backs it up for browsers
              // that do not block submission on an unfilled required field.
              // A PROJECTION is different: playbookId empty is its NORMAL
              // state right after the definition is created, before this
              // organization has synced the matching playbook (spec decision
              // 4; internal/templates/service.go's own Update already
              // accepts an empty playbook_id unchanged on a projection,
              // exactly the case refuseSharedFieldChange exists to allow).
              // Blocking Save here would also block the one thing this
              // dialog still lets the organization edit on a projection —
              // scope, the default SSH credential, the vault identity set —
              // which decision 2 gives them and decision 4 says they must be
              // able to fill in before any sync happens at all.
              disabled={(!playbookId && !projected) || create.isPending || update.isPending}
            >
              {existing ? t.templates.dialog.save : t.templates.dialog.create}
            </Button>
          </div>
        </div>
      </form>
      {/* Sibling of the <form> above, not inside it — see HostSshCard.tsx's
          own comment on why a nested <dialog> here would double-submit. */}
      {addingCredential && (
        <AddCredentialDialog
          org={org}
          open
          initialName={addingCredential.name}
          // The Vault identities list filters to vault_password, which
          // this dialog's own ssh_key default would not match — without
          // this the new credential would not appear as an option in the
          // very row that created it (the same reason RepoDialog's
          // equivalent pickers pass it).
          initialKind={addingCredential.field === 'vault-identity' ? 'vault_password' : undefined}
          onClose={() => setAddingCredential(null)}
          onCreated={(id) => {
            // Narrowed into a local const: TS forgets a discriminated union
            // narrowed from component state the moment it is read inside a
            // nested closure (the `rows.map` callback below), even though
            // `addingCredential` cannot change mid-callback.
            const adding = addingCredential;
            if (adding.field === 'ssh') setCredentialId(id);
            else setVaultRows((rows) => rows.map((r) => (r.key === adding.rowKey ? { ...r, credentialId: id } : r)));
          }}
        />
      )}
    </Dialog>
  );
}
