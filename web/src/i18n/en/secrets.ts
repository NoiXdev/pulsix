/** Settings → Secrets (SecretsPage, SecretDialog): the organization's
 * secret store, holding the credentials the checks the Pulsix server runs
 * need (an SNMP community or login). Values are write-only: nothing here
 * ever shows one. Field names, secret names and check keys are data. */
export const enSecrets = {
  title: 'Secrets',
  subtitle:
    'Credentials for the checks the Pulsix server runs, such as an SNMP community or login. A check names a secret in its Secret field.',
  count: (n: number) => (n === 1 ? '1 secret' : `${n} secrets`),
  add: 'New secret',
  readOnly: 'Only owners and admins can create, replace or delete secrets.',
  empty: 'No secrets yet. Add one for each device login a check needs, then name it in the check.',
  columns: {
    name: 'Name',
    fields: 'Fields',
    updated: 'Last change',
    usedBy: 'Used by',
  },
  unused: 'no check',
  replace: 'Replace',
  delete: 'Delete',
  deleteConfirm: (name: string) => `Delete secret ${name}? This cannot be undone.`,
  deleted: (name: string) => `Secret ${name} deleted`,
  deleteFailed: 'Failed to delete the secret',
  inUse: (name: string, checks: string) => `Secret ${name} is still used by the checks ${checks} and was not deleted.`,
  saved: (name: string) => `Secret ${name} saved`,
  saveFailed: 'Failed to save the secret',
  dialog: {
    newTitle: 'New secret',
    replaceTitle: (name: string) => `Replace ${name}`,
    name: 'Name',
    nameHint: 'Letters, digits and . _ -, up to 64 characters. Checks name the secret by it.',
    fields: 'Fields',
    fieldsHint:
      'For SNMP v2c: community. For SNMP v3: username, auth_protocol and auth_password, plus priv_protocol and priv_password for encryption.',
    fieldName: (i: number) => `Field ${i} name`,
    fieldValue: (i: number) => `Field ${i} value`,
    removeField: (i: number) => `Remove field ${i}`,
    addField: 'Add field',
    neverShown: 'Values are never shown again after saving: they are stored encrypted and only the checks naming this secret use them.',
    replaceNote: 'Replacing stores exactly the fields below: enter every value again. A field you remove is deleted.',
  },
  /** Read as the continuation of the field's label, hence the lower case. */
  errors: {
    name: 'must be 1 to 64 characters: letters, digits and . _ -',
    nameTaken: 'a secret of this name already exists — replace it from its row instead',
    fieldName: 'must be 1 to 64 characters: letters, digits and . _ -',
    fieldRepeated: 'this field name appears twice',
    valueTooLong: 'must be at most 4096 bytes',
    valueNul: 'must not contain a NUL character',
    fieldCount: 'A secret has 1 to 16 fields.',
  },
};
