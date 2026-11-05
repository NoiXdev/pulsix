import { useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
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
import { usePickRepoPlaybook } from '../../api/hooks/useRepos.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useCreateTemplate, useDeleteTemplate, useTemplate, useUpdateTemplate } from '../../api/hooks/useTemplates.ts';
import { problemsFromError } from '../variables/paths.ts';
import type { Vars } from '../variables/types.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { kindName } from '../credentials/labels.ts';
import { useT } from '../../i18n/useT.ts';
import { VaultIdentitiesField } from '../credentials/VaultIdentitiesField.tsx';
import type { Credential, Playbook, Template } from '../../api/types.ts';
import { identitiesForBody, rowsFromIdentities, vaultIdentityErrors, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { ScopePicker, type Scope } from './ScopePicker.tsx';
import { PlaybookPicker, type PlaybookChoice } from './PlaybookPicker.tsx';
import { echoedVars } from './vars.ts';

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
 * TemplateDialog creates or edits a job template's settings: name,
 * playbook (PlaybookPicker), scope (the inventory scope picker), a
 * free-form limit, the default SSH credential and the vault identities.
 * The extra vars and their schema live on the template's variables page
 * (backlog 7.75), which an edit links to; the PUT replaces them too, so it
 * echoes them as loaded — the schema unchanged and each stored secret as a
 * keep marker (templates/vars.ts) — under If-Match, and a stale revision
 * (someone saved, or a sync applied the playbook's variables) answers 412.
 */
export function TemplateDialog({ org, template, canReadCredentials, canCreateCredential, onClose }: TemplateDialogProps) {
  const detail = useTemplate(org, template?.id ?? '');
  const playbooks = usePlaybooks(org);
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const guard = useQueryGuard(detail);
  // Bumped by the stale notice's Reload once the template is read again:
  // the form takes the template as loaded when it mounts (its draft and
  // the revision that draft is based on), so a background refetch never
  // moves the base under it — only a Reload does, by remounting.
  const [generation, setGeneration] = useState(0);

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
        key={generation}
        org={org}
        existing={existingOf(tpl)}
        onReload={async () => {
          await detail.refetch();
          setGeneration((g) => g + 1);
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

function existingOf(tpl: Template): Existing {
  return {
    id: tpl.id,
    name: tpl.name,
    revision: tpl.revision,
    playbookId: tpl.playbook_id,
    scope: { group_id: tpl.scope.group_id ?? '', host_ids: tpl.scope.host_ids ?? [] },
    limit: tpl.limit,
    schema: tpl.extra_vars_schema,
    vars: tpl.extra_vars ?? {},
    credentialId: tpl.credential_id ?? '',
    vaultIdentities: rowsFromIdentities(tpl.vault_identities),
    sharedTemplateId: tpl.shared_template_id,
  };
}

interface Existing {
  id: string;
  name: string;
  /** The revision this form's draft is based on (If-Match). */
  revision: number;
  playbookId: string;
  scope: Scope;
  limit: string;
  /** The extra-vars schema exactly as loaded; the PUT sends it back. */
  schema: Template['extra_vars_schema'];
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
  org, existing, onReload, playbooks, credentials, vaultCredentials, canCreateCredential, onClose,
}: {
  org: string;
  existing?: Existing;
  /** Reads the template again and starts the form over (after a 412). */
  onReload?: () => Promise<void>;
  playbooks: readonly Playbook[];
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
  // The template as this form loaded it: a background refetch must not
  // move the revision (or the variables echoed) under the draft.
  const [loaded] = useState(existing);

  const [name, setName] = useState(existing?.name ?? '');
  const [playbook, setPlaybook] = useState<PlaybookChoice>({ from: '', playbookId: existing?.playbookId ?? '', path: '' });
  const pick = usePickRepoPlaybook(org, playbook.from === 'inline' ? '' : playbook.from);
  const [scope, setScope] = useState<Scope>(existing?.scope ?? { group_id: '', host_ids: [] });
  const [limit, setLimit] = useState(existing?.limit ?? '');
  const [credentialId, setCredentialId] = useState(existing?.credentialId ?? '');
  const [vaultRows, setVaultRows] = useState<VaultIdentityRow[]>(existing?.vaultIdentities ?? []);
  const [vaultErrors, setVaultErrors] = useState<Record<string, string>>({});
  // null = closed. One mount point for the Default SSH credential field and
  // every row of the Vault identities list, since only one AddCredentialDialog
  // can be open at a time (the same division RepoDialog's own
  // `addingCredential` keeps).
  const [addingCredential, setAddingCredential] = useState<{ field: 'ssh'; name: string } | { field: 'vault-identity'; rowKey: string; name: string } | null>(null);
  const [stale, setStale] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /** playbookIdFor is the chosen playbook's id: a repository path nobody
   * picked before is registered first (null when that failed). */
  async function playbookIdFor(): Promise<string | null> {
    if (playbook.playbookId || !playbook.path) return playbook.playbookId;
    try {
      const { playbook: picked } = await pick.mutateAsync(playbook.path);
      setPlaybook((c) => ({ ...c, playbookId: picked.id }));
      return picked.id;
    } catch (err) {
      setError(err instanceof Error ? err.message : t.templates.dialog.pickFailed);
      return null;
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const identityErrors = vaultIdentityErrors(vaultRows, t);
    setVaultErrors(identityErrors);
    if (Object.keys(identityErrors).length > 0) return;
    const playbookId = await playbookIdFor();
    if (playbookId === null) return;
    const body = {
      name,
      playbook_id: playbookId,
      scope: scope.group_id || scope.host_ids.length > 0 ? { group_id: scope.group_id || undefined, host_ids: scope.host_ids.length > 0 ? scope.host_ids : undefined } : undefined,
      limit: limit || undefined,
      extra_vars: loaded ? echoedVars(loaded.schema, loaded.vars) : {},
      extra_vars_schema: loaded?.schema,
      credential_id: credentialId || undefined,
      vault_identities: identitiesForBody(vaultRows),
    };
    try {
      if (loaded) await update.mutateAsync({ body, revision: loaded.revision });
      else await create.mutateAsync(body);
      toast.show(existing ? t.templates.dialog.saved(body.name) : t.templates.dialog.created(body.name));
      onClose();
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      // The extra vars are not edited here: a value the server refuses is
      // fixed on the variables page, so its problems are named here.
      const varPs = [...problemsFromError(err, 'body.extra_vars'), ...problemsFromError(err, 'body.extra_vars_schema')];
      if (varPs.length > 0) return setError(t.templates.dialog.varsProblems(varPs.map((p) => (p.path ? `${p.path}: ${p.message}` : p.message)).join('; ')));
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
        {stale && (
          <div role="alert" className="flex items-center justify-between gap-3 rounded-md border border-border bg-card px-3 py-2 text-xs">
            <span>{t.templates.dialog.stale}</span>
            {onReload && (
              <Button type="button" onClick={() => void onReload()}>
                {t.templates.dialog.reload}
              </Button>
            )}
          </div>
        )}
        <fieldset disabled={projected} className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="template-name">{t.templates.dialog.name}</Label>
            <Input id="template-name" className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder={t.templates.dialog.namePlaceholder} autoFocus={!existing} />
          </div>
          <PlaybookPicker org={org} playbooks={playbooks} value={playbook} onChange={setPlaybook} disabled={projected} />
        </fieldset>
        <ScopePicker org={org} value={scope} onChange={setScope} />
        <fieldset disabled={projected} className="flex flex-col gap-1">
          <Label htmlFor="template-limit">{t.templates.dialog.limit}</Label>
          <Input id="template-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder={t.templates.dialog.limitPlaceholder} />
          <span className="text-[11px] text-muted-foreground">{t.templates.dialog.limitHint}</span>
        </fieldset>
        {existing && (
          <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
            <span className="text-[11px] text-muted-foreground">{t.templates.dialog.variablesHint}</span>
            <Link
              to="/o/$org/templates/$id/variables"
              params={{ org, id: existing.id }}
              className="shrink-0 text-xs font-medium text-primary hover:underline"
            >
              {t.templates.dialog.variables}
            </Link>
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
              disabled={(!playbook.playbookId && !playbook.path && !projected) || create.isPending || update.isPending || pick.isPending}
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
