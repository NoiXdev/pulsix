import { Plus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Select } from '../../ui/Select.tsx';
import type { Credential } from '../../api/types.ts';
import { newVaultIdentityRow, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { useT } from '../../i18n/useT.ts';

export interface VaultIdentitiesFieldProps {
  rows: readonly VaultIdentityRow[];
  onChange: (rows: VaultIdentityRow[]) => void;
  /** The org's full credential list — filtered to vault_password here, the
   * same filter RepoDialog's own single Vault password field already
   * applies (only vault_password credentials are offered; the server
   * refuses anything else in this slot). */
  credentials: readonly Credential[];
  /** Gates each row's create row the same way every other credential
   * picker's does: credentials.write is admin, stricter than the
   * repos.write/templates.write that gates the surrounding dialog. */
  canCreateCredential: boolean;
  /** Keyed by row.key (vaultIdentityErrors' own shape, lib/vaultIdentity.ts). */
  errors: Record<string, string>;
  onRequestCreate: (row: VaultIdentityRow, filterText: string) => void;
  /** Explanatory copy under the heading — RepoDialog uses it to point at
   * the legacy single Vault password field still above this one;
   * TemplateDialog has no such field and passes its own note instead. */
  help: string;
}

/**
 * VaultIdentitiesField edits a repository's or template's labelled vault
 * password set (v0.2e design): each row is one label paired with a
 * vault_password credential, Ansible's own vault_identity_list entries.
 * Shared by RepoDialog and TemplateDialog — both write the same
 * VaultIdentityDTO shape and both filter to the same credential kind — so
 * the row UI, its validation surface and its create-row wiring exist once
 * rather than as two hand-kept copies.
 *
 * Presentational, like the other credentials pieces reused across dialogs
 * (AddCredentialDialog, KIND_LABEL): it holds no mutation of its own and
 * asks the caller to open AddCredentialDialog, the same division
 * RepoDialog's own `addingCredential` state already
 * keeps for its other two credential pickers — only one such dialog can be
 * mounted at a time, so the caller is the one in a position to enforce
 * that.
 */
export function VaultIdentitiesField({ rows, onChange, credentials, canCreateCredential, errors, onRequestCreate, help }: VaultIdentitiesFieldProps) {
  const t = useT();
  const options = credentials.filter((c) => c.kind === 'vault_password').map((c) => ({ value: c.id, label: c.name }));

  function update(key: string, patch: Partial<VaultIdentityRow>) {
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  }

  function removeRow(key: string) {
    onChange(rows.filter((r) => r.key !== key));
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-[11px] font-medium text-muted-foreground">{t.credentials.vault.heading}</span>
        <Button type="button" variant="ghost" onClick={() => onChange([...rows, newVaultIdentityRow()])}>
          <Plus size={13} /> {t.credentials.vault.add}
        </Button>
      </div>
      <span className="text-[11px] text-muted-foreground">{help}</span>
      {rows.length === 0 && <p className="text-xs text-muted-foreground">{t.credentials.vault.empty}</p>}
      {rows.map((row, i) => (
        <div key={row.key} className="flex flex-col gap-1 rounded-md border border-border p-2">
          <div className="flex items-center gap-2">
            <Input
              aria-label={t.credentials.vault.labelOf(i + 1)}
              className="flex-1 font-mono"
              placeholder={t.credentials.vault.labelPlaceholder}
              value={row.label}
              onChange={(e) => update(row.key, { label: e.target.value })}
              aria-invalid={errors[row.key] ? true : undefined}
            />
            <div className="flex-1">
              <Select
                aria-label={t.credentials.vault.credentialOf(i + 1)}
                options={options}
                value={row.credentialId}
                onChange={(value) => update(row.key, { credentialId: value })}
                placeholder={t.credentials.vault.choose}
                invalid={!!errors[row.key]}
                create={canCreateCredential ? { label: t.credentials.vault.newCredential, onSelect: (text) => onRequestCreate(row, text) } : undefined}
              />
            </div>
            <Button type="button" variant="ghost" aria-label={t.credentials.vault.removeLabel(i + 1)} onClick={() => removeRow(row.key)}>
              {t.common.remove}
            </Button>
          </div>
          {errors[row.key] && (
            <span role="alert" className="text-[11px] text-destructive">
              {errors[row.key]}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}
