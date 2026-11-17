/**
 * The session lock (backlog 3.92): the screen that replaces the page while
 * the session is locked behind its PIN, and the dialog that sets the PIN
 * before the first lock.
 */
export const enLock = {
  title: 'Session locked',
  intro: 'This session is locked. Enter your PIN to continue.',
  pinLabel: 'PIN',
  unlock: 'Unlock',
  unlocking: 'Unlocking…',
  signOut: 'Sign out',
  lockFailed: 'The session could not be locked. Try again.',
  wrongPin: 'Wrong PIN.',
  attemptsLeft: (n: number) => (n === 1 ? '1 attempt left' : `${n} attempts left`),
  failed: 'The session could not be unlocked. Try again.',
  endedAfterFailures: 'Too many wrong PINs: the session was ended. Sign in again.',
  setPin: {
    title: 'Set a lock PIN',
    intro: 'To lock the session you need a PIN. Whoever has the PIN can unlock the session, so keep it to yourself.',
    pinLabel: 'PIN',
    repeatLabel: 'Repeat the PIN',
    hint: '4 to 8 digits.',
    invalid: 'The PIN is 4 to 8 digits.',
    mismatch: 'The two PINs do not match.',
    submit: 'Set PIN and lock',
    cancel: 'Cancel',
    failed: 'The PIN could not be set. Try again.',
  },
  prompt: {
    title: 'Set a lock PIN',
    intro:
      'A PIN lets you lock this session when you step away, for example from the lock icon at the top. You can also set or change it later under Security.',
    submit: 'Set PIN',
    skip: 'Skip',
    skipFailed: 'Could not continue. Try again.',
  },
};
