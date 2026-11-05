/**
 * The members page (list, invite dialog, open invitations), the member
 * page (role and removal) and the page an invitation link opens. The page title is
 * `settings.tabs.members`. Role names (viewer, operator, admin, owner)
 * are product terms and stay English in every language.
 */
export const enMembers = {
  count: (n: number) => `${n} members`,
  invite: 'Invite member',
  columns: {
    member: 'Member',
    role: 'Role',
    joined: 'Joined',
    status: 'Status',
    actions: 'Actions',
  },
  disabled: 'disabled',
  active: 'active',
  remove: 'remove',
  confirmRemove: (name: string) => `Remove ${name} from the organization?`,
  removed: (name: string) => `${name} removed`,
  removeFailed: 'failed to remove member',
  inviteDialog: {
    emailLabel: 'E-mail address',
    emailPlaceholder: 'new.colleague@acme.io',
    roleLabel: 'Role',
    ownerNote:
      'Owner role cannot be granted by invitation; promote after joining. The invitee sets a password and must enrol a second factor before entering the organization.',
    send: 'Send invitation',
    sent: (email: string) => `Invitation sent to ${email}`,
    failed: 'failed to send invitation',
  },
  shareLink: {
    title: 'Share the invitation link',
    before: 'Outgoing mail is not configured on this instance, so no e-mail was sent. Give this link to',
    after: 'yourself — it works once and expires in 7 days.',
    linkLabel: 'Invitation link',
    copy: 'Copy link',
    copied: 'Invitation link copied',
    copyFailed: 'Could not copy — select the link and copy it by hand',
    done: 'Done',
  },
  rowActions: (name: string) => `Actions for ${name}`,
  open: 'Open',
  removeItem: 'Remove…',
  page: {
    email: 'E-mail',
    joined: 'Joined',
    status: 'Status',
    roleTitle: 'Role',
    roleLabel: 'Role in this organization',
    changeRole: 'Change role',
    updated: (role: string) => `Role updated to ${role}`,
    failed: 'failed to change role',
    removeHint: 'Removing the member ends their access to this organization. The account itself stays.',
    removeButton: 'Remove from organization',
    readOnly: 'Only owners and admins can change roles or remove members.',
  },
  pending: {
    title: 'Open invitations',
    invitedBy: 'Invited by',
    expires: 'Expires',
    revoke: 'revoke',
    revokeAria: (email: string) => `Revoke invitation to ${email}`,
    confirmRevoke: (email: string) => `Revoke the invitation to ${email}? Its link stops working.`,
    revoked: (email: string) => `Invitation to ${email} revoked`,
    revokeFailed: 'failed to revoke invitation',
  },
  accept: {
    joinTitle: 'Join the organization',
    acceptFailed: 'failed to accept invitation',
    invalidTitle: 'This invitation link is not valid',
    invalidBody: 'The link is incomplete. Open the link from the invitation mail again, or ask for a new invitation.',
    alreadyTitle: 'You already have an account',
    alreadyBody:
      'This address already belongs to an account. Sign in to it with its own password and second factor, then open this invitation link again to join the organization.',
    goToSignIn: 'Go to sign in',
    signedInAs: 'You are signed in as',
    signedInHint: 'The invitation must be for this address.',
    wrongAddress: 'If the invitation is for a different address, sign out and open this invitation link again.',
    join: 'Join organization',
    joinedButSignInFailed: 'You joined the organization, but signing you in failed. Try signing in.',
    joinedButSignInFailedWith: (detail: string) =>
      `You joined the organization, but signing you in failed: ${detail}. Try signing in.`,
    yourName: 'Your name',
    choosePassword: 'Choose a password',
    accept: 'Accept invitation',
  },
};
