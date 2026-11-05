import type { Template } from '../../api/types.ts';
import type { Vars } from '../variables/types.ts';
import { rowsFromIdentities, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import type { Scope } from './ScopePicker.tsx';

/** An existing template as the template dialog edits it, and the base its
 * save is built on (revision, the variables it echoes). */
export function existingOf(tpl: Template): Existing {
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

export interface Existing {
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
