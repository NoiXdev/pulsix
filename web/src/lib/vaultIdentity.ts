/**
 * Client-side mirror of internal/vaultlabel.Invalid plus
 * internal/repos.MaxVaultLabelLength (internal/repos/vault.go): the one
 * rule for what text a vault identity's label may hold. A label is
 * rendered verbatim into a generated ansible.cfg as "label@path", entries
 * joined with ", " — a comma or '@' forges an extra entry, any Unicode
 * control character (category Cc) forges an entire configuration key, and
 * any Unicode format character (category Cf — invisible by design, which
 * includes the byte-order mark U+FEFF and the bidirectional-override
 * characters around U+202E) can make the label display on an operator's
 * screen as something other than the bytes it actually is. RepoDialog and
 * TemplateDialog are the two places that write a labelled vault identity
 * set, and both import from here so a mistake reads the same message
 * before the round trip as the 400 that would otherwise name it after (the
 * messages are the server's own, not a second vocabulary invented for the
 * form).
 *
 * Go is the reference for this rule, not the other way around: a reviewer
 * found that a label beginning with U+FEFF was refused here but accepted
 * by internal/vaultlabel.Invalid, because JavaScript's String.trim() strips
 * a leading/trailing BOM as whitespace while Go's strings.TrimSpace does
 * not — a disagreement neither trim implementation was ever asked to
 * settle. internal/vaultlabel/vaultlabel_test.go carries the fuller
 * character table this file's regex is checked against.
 */

export const MAX_VAULT_LABEL_LENGTH = 100;

/** invalidVaultLabel reports why label cannot be written into ansible.cfg's
 * vault_identity_list, or '' when it is fine. */
export function invalidVaultLabel(label: string): string {
  if (label === '') return 'label is empty';
  if (label.trim() !== label) return 'label has leading or trailing whitespace';
  if (/[,@]/.test(label)) {
    return "label contains a comma or '@', either of which ansible.cfg would read as more than one label";
  }
  // \p{Cc} (control) and \p{Cf} (format, invisible by design — the
  // byte-order mark and bidirectional-override characters are both in it)
  // are the JavaScript equivalent of Go's unicode.IsControl(r) ||
  // unicode.In(r, unicode.Cf); the `u` flag is required for \p{} property
  // escapes and for correct matching of the astral-plane Cf characters.
  if (/[\p{Cc}\p{Cf}]/u.test(label)) {
    return "label contains a Unicode control or format character (for example a newline, carriage return, byte-order mark, or bidirectional-override character), which ansible.cfg could read as more than one label or which could make the label display as something other than what it is";
  }
  // Runes, not UTF-16 code units: the server counts with Go's
  // utf8.RuneCountInString (internal/repos/vault.go's own comment, matching
  // huma's maxLength:"100" validation) — spreading a string iterates by
  // code point, which agrees with a rune count for every character already
  // past the ansible.cfg check above.
  if ([...label].length > MAX_VAULT_LABEL_LENGTH) return `label exceeds ${MAX_VAULT_LABEL_LENGTH} characters`;
  return '';
}

/** One row of a vault identity editor's draft state. `key` is a stable
 * React key and error lookup, never sent to the server — label and
 * credentialId round-trip into the wire's VaultIdentityDTO ({ label,
 * credential_id }). */
export interface VaultIdentityRow {
  key: string;
  credentialId: string;
  label: string;
}

let rowSeq = 0;

/** newVaultIdentityRow is the one place a blank row is created, so every
 * row added in one dialog's lifetime — however it got added — has a key
 * distinct from every other: a counter, not Date.now(), since two rows
 * added inside the same millisecond (implausible for a human clicking, not
 * for a test) would otherwise collide and make React drop one. */
export function newVaultIdentityRow(): VaultIdentityRow {
  rowSeq += 1;
  return { key: `vault-identity-${rowSeq}`, credentialId: '', label: '' };
}

/** rowsFromIdentities seeds an editor's draft rows from a loaded repository
 * or template's own stored identities (null/absent means none, the DTO's
 * own shape) — each gets a fresh key, since VaultIdentityDTO carries none
 * of its own. */
export function rowsFromIdentities(
  identities: readonly { credential_id: string; label: string }[] | null | undefined,
): VaultIdentityRow[] {
  return (identities ?? []).map((identity) => ({ ...newVaultIdentityRow(), credentialId: identity.credential_id, label: identity.label }));
}

/**
 * vaultIdentityErrors validates one form's whole row set together, not just
 * each label alone: a duplicate only becomes visible once every row has
 * been seen, the same thing the store's own PRIMARY KEY (repo_id, label) /
 * (template_id, label) would otherwise turn into a 500-shaped surprise
 * after the round trip that the Go validators (repos.Service.
 * validateVaultIdentities, templates.Service.validateVaultCredentials) both
 * already catch before reaching the store. Keyed by row key, so two rows
 * sharing the same invalid text each get their own message instead of one
 * swallowing the other's.
 */
export function vaultIdentityErrors(rows: readonly VaultIdentityRow[]): Record<string, string> {
  const errors: Record<string, string> = {};
  const firstRowFor = new Map<string, string>();
  for (const row of rows) {
    if (!row.credentialId) {
      errors[row.key] = 'choose a credential for this vault password';
      continue;
    }
    const reason = invalidVaultLabel(row.label);
    if (reason) {
      errors[row.key] = reason;
      continue;
    }
    const firstRow = firstRowFor.get(row.label);
    if (firstRow) {
      const message = `duplicate vault identity label "${row.label}"`;
      errors[row.key] = message;
      errors[firstRow] = message;
    } else {
      firstRowFor.set(row.label, row.key);
    }
  }
  return errors;
}

/** identitiesForBody is the save body's own vault_identities: undefined
 * when the form holds no rows, so a repository or template nobody has
 * given a labelled vault password keeps sending no field at all, the same
 * shape the dialogs sent before this editor existed — rather than an
 * explicit empty array, which the server treats identically
 * (repos.legacyVaultIdentities reads "empty" and "absent" the same way)
 * but would otherwise show up in a round-trip test as an unexplained new
 * key. Call only after vaultIdentityErrors(rows) is clean: it does not
 * re-check the rows itself. */
export function identitiesForBody(rows: readonly VaultIdentityRow[]): { credential_id: string; label: string }[] | undefined {
  if (rows.length === 0) return undefined;
  return rows.map((row) => ({ credential_id: row.credentialId, label: row.label }));
}
