/**
 * The account's security page (passkeys, authenticator app, recovery
 * codes) and the step-up dialog that re-asks for a second factor. The
 * Language card is `language.*`.
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
  stepUp: {
    title: 'Confirm it is you',
    intro:
      'Changing how you sign in needs your second factor again. This keeps an unattended screen from being enough to take the account over.',
    codeLabel: 'Authenticator or recovery code',
    failed: 'could not confirm your second factor',
  },
};
