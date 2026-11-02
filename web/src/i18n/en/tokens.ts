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
};
