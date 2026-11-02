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
