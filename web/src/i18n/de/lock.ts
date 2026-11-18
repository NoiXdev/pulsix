import type { Messages } from '../types.ts';

export const deLock: Messages['lock'] = {
  title: 'Sitzung gesperrt',
  intro: 'Diese Sitzung ist gesperrt. Gib deine PIN ein, um fortzufahren.',
  pinLabel: 'PIN',
  unlock: 'Entsperren',
  unlocking: 'Entsperre …',
  signOut: 'Abmelden',
  lockFailed: 'Die Sitzung konnte nicht gesperrt werden. Versuche es erneut.',
  wrongPin: 'Falsche PIN.',
  attemptsLeft: (n: number) => (n === 1 ? 'Noch 1 Versuch' : `Noch ${n} Versuche`),
  legacyNote:
    'Deine PIN stammt aus einer älteren Version und ist länger als 4 Ziffern. Lege nach dem Entsperren unter Sicherheit eine neue 4-stellige PIN fest.',
  failed: 'Die Sitzung konnte nicht entsperrt werden. Versuche es erneut.',
  endedAfterFailures: 'Zu viele falsche PINs: Die Sitzung wurde beendet. Melde dich neu an.',
  setPin: {
    title: 'Sperr-PIN festlegen',
    intro: 'Zum Sperren der Sitzung brauchst du eine PIN. Wer die PIN kennt, kann die Sitzung entsperren – behalte sie für dich.',
    pinLabel: 'PIN',
    repeatLabel: 'PIN wiederholen',
    hint: '4 Ziffern. Nimm nicht 1234 oder 0000.',
    mismatch: 'Die PINs stimmen nicht überein. Bitte noch einmal wiederholen.',
    submit: 'PIN festlegen und sperren',
    cancel: 'Abbrechen',
    failed: 'Die PIN konnte nicht festgelegt werden. Versuche es erneut.',
  },
  prompt: {
    title: 'Sperr-PIN festlegen',
    intro:
      'Mit einer PIN kannst du diese Sitzung sperren, wenn du dich entfernst, zum Beispiel über das Schloss-Symbol oben. Du kannst sie auch später unter Sicherheit festlegen oder ändern.',
    submit: 'PIN festlegen',
    skip: 'Überspringen',
    skipFailed: 'Es konnte nicht fortgefahren werden. Versuche es erneut.',
  },
};
