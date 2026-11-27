/**
 * The account's security page (passkeys, authenticator app, recovery
 * codes, password) and the step-up dialog that re-asks for a second
 * factor, and the change-password screen for an account that must replace
 * a temporary password. The Language card is `language.*`; the labels of
 * a new password typed twice are `auth.*`, shared with the reset page.
 */
export const enAccount = {
  /** The personal settings area's sidebar (ui/shell-nav.tsx). */
  nav: {
    label: 'My account',
    back: (org: string) => `Back to ${org}`,
    roleIn: (role: string, org: string) => `${role} · ${org}`,
    security: 'Security',
    appearance: 'Appearance',
    profile: 'Profile',
    tokens: 'API tokens',
    activity: 'My activity',
  },
  title: 'Security',
  description: 'How you sign in and how you protect your session.',
  secondFactorActive: 'Second factor active',
  secondFactorRequired: 'Second factor required',
  /** No factor, on an instance where it is optional (PULSIX_MFA_OPTIONAL). */
  secondFactorOptional: 'Second factor optional',
  /** The first factor of an account signed in on its password alone
   * revoked the account's API tokens (tokens_revoked). */
  tokensRevokedOnFirstFactor:
    'Second factor set up. API tokens created before it were revoked: create new ones if you still need them.',
  signIn: { title: 'Sign-in', description: 'How you sign in: password plus a second factor; passkeys replace both.' },
  session: {
    title: 'Session',
    description: 'Away from your desk? Lock the session with the lock at the top instead of signing out.',
    noPin: 'No PIN',
    thisSession: 'This session',
    expires: (time: string) => `Expires at ${time}`,
    lockNow: 'Lock now',
  },
  confirm: 'Confirm',
  passkeys: {
    title: 'Passkeys',
    intro: 'Sign in with your fingerprint, your face or a security key, with nothing to type.',
    added: (date: string) => `added ${date}`,
    removeAria: (name: string) => `remove ${name}`,
    removed: (name: string) => `Passkey ${name} removed`,
    removeFailed: 'failed to remove passkey',
    count: (n: number) => (n === 0 ? 'None' : `${n} registered`),
    dialogTitle: 'Add a passkey',
    nameLabel: 'Passkey name',
    register: 'Register',
    cancel: 'Cancel',
    addedToast: 'Passkey added',
    addFailed: 'failed to add passkey',
    namePlaceholder: 'e.g. MacBook Pro',
    add: 'Add passkey…',
  },
  totp: {
    title: 'Authenticator app',
    intro: 'A six-digit code from an app on your phone, with recovery codes in case you lose it.',
    status: 'Set up',
    notSetUp: 'Not set up',
    enrol: 'Set up…',
    enrolAgain: 'Set up again…',
    dialogTitle: 'Set up the authenticator app',
    enrolFailed: 'failed to start enrolment',
    scanHint: 'Scan with any TOTP app, or enter the secret manually, then confirm with a 6-digit code.',
  },
  recovery: {
    title: 'Recovery codes',
    left: 'left',
    leftHint: 'each works one time, and they are the way back in if you lose your authenticator.',
    regenerate: 'Regenerate…',
    regenerateConfirm: 'Your current recovery codes stop working.',
    regenerateConfirmLabel: 'Regenerate',
    dialogTitle: 'Recovery codes',
    regenerateFailed: 'Could not regenerate the recovery codes.',
    shownOnce: 'Recovery codes – shown once. Each works one time.',
    saved: 'I have saved these',
  },
  password: {
    title: 'Password',
    intro: 'Changing the password signs you out everywhere else; this session stays.',
    current: 'Current password',
    temporary: 'Temporary password',
    submit: 'Change password',
    open: 'Change password…',
    changed: 'Password changed. Your other sessions were signed out.',
    invalidCurrent: 'The current password is not correct.',
    invalidTemporary: 'The temporary password is not correct.',
    temporaryExpired: 'Your temporary password has expired. Ask an instance administrator for a new one.',
    sameAsCurrent: 'The new password must differ from the current one.',
    sameAsTemporary: 'The new password must differ from the temporary one.',
    failed: 'The password could not be changed.',
  },
  forced: {
    title: 'Choose a new password',
    intro: (email: string) =>
      `An administrator set a temporary password for ${email}. Choose a password of your own to continue.`,
    signOut: 'Sign out',
  },
  stepUp: {
    title: 'Confirm it is you',
    intro:
      'Changing how you sign in needs your second factor again. This keeps an unattended screen from being enough to take the account over.',
    codeLabel: 'Authenticator or recovery code',
    /** For an account without a second factor while it is optional: the
     * password is its step-up. */
    introPassword:
      'Changing how you sign in needs your password again. This keeps an unattended screen from being enough to take the account over.',
    passwordLabel: 'Password',
    failed: 'could not confirm your second factor',
    refusedAgain: 'Confirming your identity did not work. Try again.',
  },
  lockPin: {
    title: 'Lock PIN',
    help: '4 digits. Changing or removing it asks for your second factor.',
    legacy: 'Older PIN',
    legacyHelp: 'Set a new 4-digit PIN.',
    set: 'Set PIN…',
    change: 'Change PIN…',
    remove: 'Remove PIN',
    dialogSet: 'Set a lock PIN',
    dialogChange: 'Change the lock PIN',
    cancel: 'Cancel',
    saved: 'Lock PIN saved.',
    removed: 'Lock PIN removed.',
    saveFailed: 'The PIN could not be saved. Try again.',
    removeFailed: 'The PIN could not be removed. Try again.',
  },
};
