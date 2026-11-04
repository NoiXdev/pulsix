/**
 * The account's security page (passkeys, authenticator app, recovery
 * codes, password) and the step-up dialog that re-asks for a second
 * factor, and the change-password screen for an account that must replace
 * a temporary password. The Language card is `language.*`; the labels of
 * a new password typed twice are `auth.*`, shared with the reset page.
 */
export const enAccount = {
  title: 'Security',
  secondFactorActive: 'second factor active',
  secondFactorRequired: 'second factor required',
  confirm: 'Confirm',
  passkeys: {
    title: 'Passkeys',
    added: (date: string) => `added ${date}`,
    removeAria: (name: string) => `remove ${name}`,
    removed: (name: string) => `Passkey ${name} removed`,
    removeFailed: 'failed to remove passkey',
    none: 'No passkeys registered yet.',
    addedToast: 'Passkey added',
    addFailed: 'failed to add passkey',
    namePlaceholder: 'e.g. MacBook Pro',
    add: 'Add passkey',
  },
  totp: {
    title: 'Authenticator app',
    enrol: 'Enrol authenticator app',
    enrolFailed: 'failed to start enrolment',
    scanHint: 'Scan with any TOTP app, or enter the secret manually, then confirm with a 6-digit code.',
  },
  recovery: {
    left: 'Recovery codes left:',
    leftHint: 'each works one time, and they are the way back in if you lose your authenticator.',
    regenerate: 'Regenerate recovery codes',
    regenerateFailed: 'Could not regenerate the recovery codes.',
    shownOnce: 'Recovery codes — shown once. Each works one time.',
    saved: 'I have saved these',
  },
  password: {
    title: 'Password',
    intro: 'Changing the password signs you out everywhere else; this session stays.',
    current: 'Current password',
    temporary: 'Temporary password',
    submit: 'Change password',
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
    failed: 'could not confirm your second factor',
  },
};
