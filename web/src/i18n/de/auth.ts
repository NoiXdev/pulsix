import type { Messages } from '../types.ts';

export const deAuth: Messages['auth'] = {
  signIn: 'Anmelden',
  emailAddress: 'E-Mail-Adresse',
  password: 'Passwort',
  usePasskey: 'Passkey verwenden',
  usePasskeyInstead: 'Stattdessen Passkey verwenden',
  backToSignIn: 'Zurück zur Anmeldung',
  secondFactorTitle: 'Code für den zweiten Faktor eingeben',
  sixDigitCode: '6-stelliger Code',
  recoveryCode: 'Wiederherstellungscode',
  verify: 'Bestätigen',
  useRecoveryCode: 'Wiederherstellungscode verwenden',
  useSixDigitCode: 'Stattdessen 6-stelligen Code verwenden',
  errors: {
    signInFailed: 'Anmeldung fehlgeschlagen',
    verificationFailed: 'Bestätigung fehlgeschlagen',
    passkeySignInCancelled: 'Anmeldung mit Passkey abgebrochen',
    invalidCredentials: 'ungültige Anmeldedaten',
    invalidCode: 'ungültiger Code',
    invalidRecoveryCode: 'ungültiger Wiederherstellungscode',
    tooManyAttempts: 'zu viele Versuche; einige Minuten warten und erneut versuchen',
  },
  passkey: {
    registrationCancelled: 'Registrierung des Passkeys abgebrochen',
    loginCancelled: 'Anmeldung mit Passkey abgebrochen',
  },
};
