/** API tokens (TokensPage). Role names are data and stay as the API
 * writes them. */
export const enTokens = {
  title: 'API tokens',
  create: 'Create token',
  count: (n: number) => `${n} tokens`,
  columns: {
    name: 'Name',
    role: 'Role',
    lastUsed: 'Last used',
    expires: 'Expires',
  },
  revoke: 'revoke',
  revokeConfirm: (name: string) => `Revoke token ${name}?`,
  revoked: (name: string) => `Token ${name} revoked`,
  revokeFailed: 'failed to revoke token',
  createFailed: 'failed to create token',
  empty: 'No tokens yet.',
  shownOnce: 'Copy this token now — it is shown exactly once and cannot be retrieved again.',
  copy: 'Copy',
  done: 'Done',
  nameLabel: 'Name',
  roleLabel: 'Role',
  roleHint: 'Capped at your own role — the server never mints a token more privileged than you.',
  /** The kind of token: an API token for the REST API, or an assistant
   * token limited to the assistant endpoint and the ticked areas. */
  kind: {
    label: 'Kind',
    api: 'API token',
    assistant: 'Assistant token',
    apiHint: 'For scripts and integrations: works with everything the role allows.',
    assistantHint:
      'For an AI assistant: works only through the assistant access of this organization, and only in the areas ticked below. Runs it requests need a person to approve them.',
    badge: 'Assistant',
  },
  areas: {
    legend: 'Areas',
    area: 'Area',
    read: 'read',
    write: 'write',
    request: 'request runs',
    rows: {
      inventory: 'Inventory',
      repos: 'Repositories & playbooks',
      templates: 'Job templates',
      jobs: 'Jobs',
    },
    credentials: 'Credentials: names only',
    /** The accessible name of one checkbox in the grid. */
    cell: (row: string, column: string) => `${row}: ${column}`,
    writeHint: 'Write includes read. Credential names show which credentials exist, never their contents.',
    roleHint: (role: string) => `Greyed-out areas need a higher role than ${role}.`,
    required: 'Tick at least one area.',
    /** How the token list names each area a token holds. */
    labels: {
      'inventory.read': 'Inventory: read',
      'inventory.write': 'Inventory: write',
      'repos.read': 'Repositories & playbooks: read',
      'repos.write': 'Repositories & playbooks: write',
      'templates.read': 'Job templates: read',
      'templates.write': 'Job templates: write',
      'credentials.names': 'Credentials: names only',
      'jobs.read': 'Jobs: read',
      'jobs.request': 'Jobs: request runs',
    },
  },
};
