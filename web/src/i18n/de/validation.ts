import type { Messages } from '../types.ts';

export const deValidation: Messages['validation'] = {
  required: 'Pflichtfeld',
  maxChars: (max: number) => `höchstens ${max} Zeichen`,
  minChars: (min: number) => `mindestens ${min} Zeichen`,
  slugShape: 'nur Kleinbuchstaben, Ziffern und einzelne Bindestriche',
  email: 'gültige E-Mail-Adresse eingeben',
  port: 'Port zwischen 1 und 65535 eingeben',
  selectZone: 'Zone auswählen',
  passwordsMismatch: 'Passwörter stimmen nicht überein',
};
