/**
 * The sign-in page, its second-factor step and the passkey ceremonies'
 * own errors. Error texts live here, not in loginMachine.ts / passkeys.ts,
 * which take them as a parameter (defaulting to English) so the state
 * machine stays free of React.
 */
export const enAuth = {
  signIn: 'Sign in',
  emailAddress: 'Email address',
  password: 'Password',
  usePasskey: 'Use a passkey',
  usePasskeyInstead: 'Use a passkey instead',
  backToSignIn: 'Back to sign in',
  secondFactorTitle: 'Enter your second-factor code',
  sixDigitCode: '6-digit code',
  recoveryCode: 'Recovery code',
  verify: 'Verify',
  useRecoveryCode: 'Use a recovery code',
  useSixDigitCode: 'Use a 6-digit code instead',
  forgotPassword: 'Forgot password?',
  newPassword: 'New password',
  repeatNewPassword: 'Repeat the new password',
  minLengthHint: (min: number) => `At least ${min} characters.`,
  forgot: {
    title: 'Reset your password',
    intro: 'Enter the e-mail address of your account, and we send you a link to choose a new password.',
    submit: 'Send link',
    sent: 'If an account exists for this address, we sent a link to it. The link works once, for 60 minutes.',
    unavailable:
      'Resetting a password by e-mail is not available on this instance. Please ask an instance administrator to set a temporary password for you.',
    checkFailed: 'Could not check whether a reset link can be sent. Try again later.',
    failed: 'The request could not be sent. Try again.',
    backToSignIn: 'Back to sign in',
  },
  reset: {
    title: 'Choose a new password',
    submit: 'Set password',
    doneTitle: 'Your password is set',
    done: 'Sign in with your new password and your second factor.',
    goToSignIn: 'Go to sign in',
    invalidLink: 'This link is invalid or has expired. A link works once, for 60 minutes, and a newer link replaces it.',
    requestNew: 'Request a new link',
    failed: 'The password could not be set.',
  },
  errors: {
    signInFailed: 'sign in failed',
    verificationFailed: 'verification failed',
    passkeySignInCancelled: 'passkey sign-in was cancelled',
    invalidCredentials: 'invalid credentials',
    invalidCode: 'invalid code',
    invalidRecoveryCode: 'invalid recovery code',
    tooManyAttempts: 'too many attempts; wait a few minutes and try again',
  },
  passkey: {
    registrationCancelled: 'passkey registration was cancelled',
    loginCancelled: 'passkey login was cancelled',
  },
};
