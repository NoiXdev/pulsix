/** Zone workers (WorkersPage, AddWorkerDialog) and the age/expiry wording
 * workers/expiry.ts builds — agents, hosts and the fleet page reuse that
 * helper, so `time` is shared. Statuses are the API's own values mapped to
 * a label; an unknown one is shown as the API wrote it. */
export const enWorkers = {
  time: {
    never: 'never',
    justNow: 'just now',
    minutesAgo: (n: number) => `${n} min ago`,
    hoursAgo: (n: number) => `${n} h ago`,
    daysAgo: (n: number) => `${n} d ago`,
    expired: 'expired',
    today: 'today',
    inDays: (n: number) => `in ${n} d`,
  },
  title: 'Workers',
  noAccess: "Workers are managed by the organization's owners and admins.",
  summary: (online: number, total: number) => `${online} of ${total} online · workers run the jobs of their zone`,
  add: 'Add worker',
  columns: {
    worker: 'Worker',
    zone: 'Zone',
    status: 'Status',
    lastSeen: 'Last seen',
    version: 'Version',
    certificate: 'Certificate',
    currentJob: 'Current job',
    pendingEnrollment: 'Pending enrollment',
    expires: 'Expires',
  },
  status: {
    online: 'online',
    offline: 'offline',
    revoked: 'revoked',
  },
  // The badge next to a build version (also used by the Agents page and the
  // host agent card): only the two states that need an operator's attention.
  versionStatus: {
    update_required: 'Update required',
    newer: 'Newer than manager',
  },
  empty: 'No workers yet.',
  revokeItem: 'Revoke…',
  revokeConfirm: (name: string) => `Revoke worker ${name}? It is cut off at once; its running jobs go back to the queue.`,
  revoked: (name: string) => `Worker ${name} revoked`,
  revokeFailed: 'Revoking failed',
  deleteItem: 'Delete…',
  deleteConfirm: (name: string) => `Delete worker ${name}? Its history stays in the activity log.`,
  deleteFailed: 'Deleting failed',
  withdraw: 'Withdraw',
  withdrawFailed: 'Withdrawing failed',
  dialog: {
    title: 'Add worker',
    titleNamed: (name: string) => `Add worker ${name}`,
    nameRule: 'Use lowercase letters, digits and hyphens, starting with a letter or digit.',
    createFailed: 'Creating the token failed',
    tokenNote:
      'Token valid for one hour, single use. The worker connects outbound, receives its certificate and appears here. The token is shown only now.',
    tokenLabel: 'Enrollment token',
    copyToken: 'Copy token',
    snippetLabel: 'Worker snippet',
    copySnippet: 'Copy snippet',
    copy: 'Copy',
    done: 'Done',
    docker: 'Docker',
    compose: 'Compose service',
    composeUp: 'Start it with',
    composeUpLabel: 'Start command',
    copyComposeUp: 'Copy start command',
    composeUpHint:
      'The token stays out of the file: it is set only for this command, so the file can be saved or shared.',
    zone: 'Zone',
    newZone: 'New zone…',
    name: 'Name',
    namePlaceholder: 'hel-1',
    createToken: 'Create token',
  },
};
