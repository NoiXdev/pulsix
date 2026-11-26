import { useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePickRepoPlaybook } from '../../api/hooks/useRepos.ts';
import { useCreateTemplate, useUpdateTemplate } from '../../api/hooks/useTemplates.ts';
import type { Credential, Playbook } from '../../api/types.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { identitiesForBody, vaultIdentityErrors, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { useT } from '../../i18n/useT.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { kindName } from '../credentials/labels.ts';
import { VaultIdentitiesField } from '../credentials/VaultIdentitiesField.tsx';
import { problemsFromError } from '../variables/paths.ts';
import { ScopePicker, type Scope } from './ScopePicker.tsx';
import { PlaybookPicker, type PlaybookChoice } from './PlaybookPicker.tsx';
import { echoedVars } from './vars.ts';
import { mergeScope, mergeSetting, mergeVaultRows } from './rebase.ts';
import { FollowUpsField } from './FollowUpsField.tsx';
import { useDeleteTemplateCommand } from './useDeleteTemplateCommand.ts';
import type { Existing } from './existing.ts';

export interface TemplateFormProps {
  org: string;
  /** The template to edit; absent means create. */
  existing?: Existing;
  /** Reads the template again (after a 412); the form re-bases on it. */
  onReload?: () => Promise<Existing | undefined>;
  playbooks: readonly Playbook[];
  /** The Default SSH credential field's choices: SSH keys and passwords. */
  credentials: readonly { id: string; name: string; kind: string }[];
  /** The org's full credential list, unfiltered — `credentials` above is
   * already narrowed to ssh_key/ssh_password for the Default SSH
   * credential field, which excludes every vault_password credential the
   * Vault identities list below needs to offer. */
  vaultCredentials: readonly Credential[];
  canCreateCredential: boolean;
  canEnrolAgents: boolean;
  /** Create only: the playbook the new template starts with. */
  initialPlaybookId?: string;
  /** Where the form sits. `dialog` (the template dialog) scrolls inside the
   * dialog, links an existing template's variables and offers Delete;
   * `page` (the template page's Settings tab) leaves both to the page:
   * the variables are a tab of their own, Delete is the header's. */
  variant: 'dialog' | 'page';
  /** Every field disabled and no actions: the Settings tab before Edit. */
  readOnly?: boolean;
  /** Cancel. */
  onCancel: () => void;
  /** After the template was saved, created or deleted. */
  onDone: () => void;
}

/**
 * TemplateForm is a job template's settings form, the one the template
 * dialog (creating, and editing from the lists) and the template page's
 * Settings tab share: name, playbook (PlaybookPicker), scope (the inventory
 * scope picker), a free-form limit, the default SSH credential, the vault
 * identities, the follow-ups and agent enrollment. The extra vars and their
 * schema live on the template's variables page (backlog 7.75); the PUT
 * replaces them too, so it echoes them as loaded — the schema unchanged and
 * each stored secret as a keep marker (templates/vars.ts) — under If-Match,
 * and a stale revision (someone saved, or a sync applied the playbook's
 * variables) answers 412, which offers Reload: a field-by-field re-base
 * that keeps this person's changes (rebase.ts).
 */
export function TemplateForm({
  org, existing, onReload, playbooks, credentials, vaultCredentials, canCreateCredential, canEnrolAgents, initialPlaybookId, variant, readOnly = false, onCancel, onDone,
}: TemplateFormProps) {
  const t = useT();
  const toast = useToast();
  const create = useCreateTemplate(org);
  const update = useUpdateTemplate(org, existing?.id ?? '');
  const remove = useDeleteTemplateCommand(org);
  // Set means this row is a projection of an installation-owned definition
  // (spec v0.2f decision 1): name, playbook, limit, extra vars and their
  // schema are the definition's own, refused on write by templates.Service.
  // Update (refuseSharedFieldChange, internal/templates/service.go)
  // regardless of what this form sends, so they are disabled rather than
  // merely validated client-side — RepoDialog's own `projected` flag, same
  // reasoning.
  const projected = Boolean(existing?.sharedTemplateId);
  // The template this form's save is based on: its revision (If-Match)
  // and the variables it echoes. A background refetch must not move it;
  // the stale notice's Reload re-bases it on the template as it is now and
  // keeps the person's edits to the settings (this form edits no
  // variables, so the variables echoed are the current ones).
  const [loaded, setLoaded] = useState(existing);

  const [name, setName] = useState(existing?.name ?? '');
  const [playbook, setPlaybook] = useState<PlaybookChoice>({ from: '', playbookId: existing?.playbookId ?? initialPlaybookId ?? '', path: '' });
  const pick = usePickRepoPlaybook(org, playbook.from === 'inline' ? '' : playbook.from);
  const [scope, setScope] = useState<Scope>(existing?.scope ?? { group_id: '', host_ids: [] });
  const [limit, setLimit] = useState(existing?.limit ?? '');
  const [credentialId, setCredentialId] = useState(existing?.credentialId ?? '');
  const [vaultRows, setVaultRows] = useState<VaultIdentityRow[]>(existing?.vaultIdentities ?? []);
  const [followUps, setFollowUps] = useState<string[]>(existing?.followUps ?? []);
  const [agentEnrollment, setAgentEnrollment] = useState(existing?.agentEnrollment ?? false);
  const [followUpsError, setFollowUpsError] = useState<string | null>(null);
  const [vaultErrors, setVaultErrors] = useState<Record<string, string>>({});
  // null = closed. One mount point for the Default SSH credential field and
  // every row of the Vault identities list, since only one AddCredentialDialog
  // can be open at a time (the same division RepoDialog's own
  // `addingCredential` keeps).
  const [addingCredential, setAddingCredential] = useState<{ field: 'ssh'; name: string } | { field: 'vault-identity'; rowKey: string; name: string } | null>(null);
  const [stale, setStale] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inDialog = variant === 'dialog';

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

  /** rebase reads the template again after a 412 and merges it in field
   * by field: a setting this person changed keeps their value, every other
   * one takes the current value (a three-way merge against the base the
   * form was loaded from, rebase.ts); the save is then based on the new
   * revision. The merge reads the drafts as they are when the read
   * returns, so what the person typed meanwhile is kept. */
  async function rebase() {
    const base = loaded;
    const fresh = await onReload?.();
    if (!fresh || !base) return;
    setName((d) => mergeSetting(d, base.name, fresh.name));
    setPlaybook((d) => (d.playbookId === base.playbookId ? { from: '', playbookId: fresh.playbookId, path: '' } : d));
    setScope((d) => mergeScope(d, base.scope, fresh.scope));
    setLimit((d) => mergeSetting(d, base.limit, fresh.limit));
    setCredentialId((d) => mergeSetting(d, base.credentialId, fresh.credentialId));
    setVaultRows((d) => mergeVaultRows(d, base.vaultIdentities, fresh.vaultIdentities));
    setFollowUps((d) => mergeSetting(d, base.followUps, fresh.followUps));
    setAgentEnrollment((d) => mergeSetting(d, base.agentEnrollment, fresh.agentEnrollment));
    setLoaded(fresh);
    setStale(false);
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (readOnly) return;
    setError(null);
    setFollowUpsError(null);
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
      follow_ups: followUps.map((template_id) => ({ template_id })),
      // Sent only when switched: an omitted flag keeps the stored one, so a
      // save by someone who may not change it never touches it.
      ...(agentEnrollment !== (loaded?.agentEnrollment ?? false) ? { agent_enrollment: agentEnrollment } : {}),
    };
    try {
      if (loaded) await update.mutateAsync({ body, revision: loaded.revision });
      else await create.mutateAsync(body);
      toast.show(existing ? t.templates.dialog.saved(body.name) : t.templates.dialog.created(body.name));
      onDone();
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      // A refused follow-up list (a cycle, too many, a template deleted
      // meanwhile) is shown at its field.
      const followUpPs = problemsFromError(err, 'body.follow_ups');
      if (followUpPs.length > 0) return setFollowUpsError(followUpPs.map((p) => p.message).join('; '));
      // The extra vars are not edited here: a value the server refuses is
      // fixed on the variables page, so its problems are named here.
      const varPs = [...problemsFromError(err, 'body.extra_vars'), ...problemsFromError(err, 'body.extra_vars_schema')];
      if (varPs.length > 0) return setError(t.templates.dialog.varsProblems(varPs.map((p) => (p.path ? `${p.path}: ${p.message}` : p.message)).join('; ')));
      setError(err instanceof Error ? err.message : t.templates.dialog.saveFailed);
    }
  }

  return (
    <>
      <form onSubmit={submit} aria-label={inDialog ? undefined : t.templates.page.settings.label} className={inDialog ? 'flex max-h-[70vh] flex-col gap-4 overflow-y-auto' : 'flex max-w-3xl flex-col gap-4'}>
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
              <Button
                type="button"
                onClick={() => void rebase()}
              >
                {t.templates.dialog.reload}
              </Button>
            )}
          </div>
        )}
        {/* Read-only (the Settings tab before Edit) disables every field
            at once: a disabled fieldset disables the controls inside it. */}
        <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
          <fieldset disabled={projected} className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="template-name">{t.templates.dialog.name}</Label>
              <Input id="template-name" className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder={t.templates.dialog.namePlaceholder} autoFocus={!existing} />
            </div>
            <PlaybookPicker org={org} playbooks={playbooks} value={playbook} onChange={setPlaybook} disabled={projected || readOnly} />
          </fieldset>
          <ScopePicker org={org} value={scope} onChange={setScope} />
          <fieldset disabled={projected} className="flex flex-col gap-1">
            <Label htmlFor="template-limit">{t.templates.dialog.limit}</Label>
            <Input id="template-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder={t.templates.dialog.limitPlaceholder} />
            <span className="text-[11px] text-muted-foreground">{t.templates.dialog.limitHint}</span>
          </fieldset>
          {existing && inDialog && (
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
              // TemplateDialog), and nothing stops two of either kind
              // sharing a name.
              options={credentials.map((c) => ({ value: c.id, label: c.name, hint: kindName(c.kind, t) }))}
              value={credentialId}
              onChange={setCredentialId}
              placeholder={t.templates.dialog.credentialNone}
              disabled={readOnly}
              create={canCreateCredential && !readOnly ? { label: t.templates.dialog.newCredential, onSelect: (text) => setAddingCredential({ field: 'ssh', name: text }) } : undefined}
            />
            <span className="text-[11px] text-muted-foreground">{t.templates.dialog.credentialHint}</span>
          </div>
          <VaultIdentitiesField
            rows={vaultRows}
            onChange={setVaultRows}
            credentials={vaultCredentials}
            canCreateCredential={canCreateCredential && !readOnly}
            errors={vaultErrors}
            onRequestCreate={(row, text) => setAddingCredential({ field: 'vault-identity', rowKey: row.key, name: text })}
            help={t.templates.dialog.vaultHelp}
          />
          <div className="flex flex-col gap-1">
            <FollowUpsField
              org={org}
              id="template-follow-ups"
              value={followUps}
              onChange={setFollowUps}
              excludeId={existing?.id}
              invalid={followUpsError !== null}
              describedBy={followUpsError ? 'template-follow-ups-error' : undefined}
            />
            {followUpsError && (
              <p id="template-follow-ups-error" role="alert" className="text-xs text-destructive">
                {followUpsError}
              </p>
            )}
          </div>
          <div className="flex items-start justify-between gap-3 rounded-md border border-border px-3 py-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="template-enrol">{t.templates.dialog.enrol}</Label>
              <span id="template-enrol-help" className="text-[11px] text-muted-foreground">
                {t.templates.dialog.enrolHelp}
              </span>
            </div>
            <Switch
              id="template-enrol"
              aria-label={t.templates.dialog.enrol}
              aria-describedby="template-enrol-help"
              checked={agentEnrollment}
              disabled={!canEnrolAgents || readOnly}
              onCheckedChange={setAgentEnrollment}
            />
          </div>
        </fieldset>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        {!readOnly && (
          <div className={inDialog ? 'flex items-center justify-between gap-2' : 'flex items-center justify-end gap-2'}>
            {inDialog && existing && !projected && (
              <Button
                type="button"
                variant="destructive"
                disabled={remove.isPending}
                onClick={async () => {
                  if (await remove.run(existing)) onDone();
                }}
              >
                {t.common.delete}
              </Button>
            )}
            <div className="flex gap-2">
              <Button type="button" onClick={onCancel}>
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
                // form still lets the organization edit on a projection —
                // scope, the default SSH credential, the vault identity set —
                // which decision 2 gives them and decision 4 says they must be
                // able to fill in before any sync happens at all.
                disabled={(!playbook.playbookId && !playbook.path && !projected) || create.isPending || update.isPending || pick.isPending}
              >
                {existing ? t.templates.dialog.save : t.templates.dialog.create}
              </Button>
            </div>
          </div>
        )}
      </form>
      {/* Sibling of the <form> above, not inside it — see HostSshForm.tsx's
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
    </>
  );
}
