import type { Messages } from '../types.ts';

export const deAccount: Messages['account'] = {
  title: 'Sicherheit',
  secondFactorActive: 'zweiter Faktor aktiv',
  secondFactorRequired: 'zweiter Faktor erforderlich',
  confirm: 'Bestätigen',
  passkeys: {
    title: 'Passkeys',
    added: (date: string) => `hinzugefügt am ${date}`,
    removeAria: (name: string) => `${name} entfernen`,
    removed: (name: string) => `Passkey ${name} entfernt`,
    removeFailed: 'Passkey konnte nicht entfernt werden',
    none: 'Noch keine Passkeys registriert.',
    addedToast: 'Passkey hinzugefügt',
    addFailed: 'Passkey konnte nicht hinzugefügt werden',
    namePlaceholder: 'z. B. MacBook Pro',
    add: 'Passkey hinzufügen',
  },
  totp: {
    title: 'Authenticator-App',
    enrol: 'Authenticator-App einrichten',
    enrolFailed: 'Einrichtung konnte nicht gestartet werden',
    scanHint:
      'Mit einer beliebigen TOTP-App scannen oder den geheimen Schlüssel manuell eingeben und anschließend mit einem 6-stelligen Code bestätigen.',
  },
  recovery: {
    left: 'Verbleibende Wiederherstellungscodes:',
    leftHint: 'jeder gilt nur einmal, und sie sind der Weg zurück ins Konto, falls der Authenticator verloren geht.',
    regenerate: 'Wiederherstellungscodes neu erzeugen',
    regenerateFailed: 'Die Wiederherstellungscodes konnten nicht neu erzeugt werden.',
    shownOnce: 'Wiederherstellungscodes — werden nur einmal angezeigt. Jeder gilt nur einmal.',
    saved: 'Ich habe sie gespeichert',
  },
  password: {
    title: 'Passwort',
    intro: 'Eine Änderung des Passworts meldet alle anderen Sitzungen ab; diese Sitzung bleibt.',
    current: 'Aktuelles Passwort',
    temporary: 'Temporäres Passwort',
    submit: 'Passwort ändern',
    changed: 'Passwort geändert. Die anderen Sitzungen wurden abgemeldet.',
    invalidCurrent: 'Das aktuelle Passwort ist nicht richtig.',
    invalidTemporary: 'Das temporäre Passwort ist nicht richtig.',
    temporaryExpired: 'Das temporäre Passwort ist abgelaufen. Einen Instanz-Administrator um ein neues bitten.',
    sameAsCurrent: 'Das neue Passwort muss sich vom aktuellen unterscheiden.',
    sameAsTemporary: 'Das neue Passwort muss sich vom temporären unterscheiden.',
    failed: 'Das Passwort konnte nicht geändert werden.',
  },
  forced: {
    title: 'Neues Passwort wählen',
    intro: (email: string) =>
      `Ein Administrator hat ein temporäres Passwort für ${email} gesetzt. Zum Fortfahren ein eigenes Passwort wählen.`,
    signOut: 'Abmelden',
  },
  stepUp: {
    title: 'Identität bestätigen',
    intro:
      'Zum Ändern der Anmeldung wird der zweite Faktor erneut benötigt. So reicht ein unbeaufsichtigter Bildschirm nicht aus, um das Konto zu übernehmen.',
    codeLabel: 'Code der Authenticator-App oder Wiederherstellungscode',
    failed: 'Zweiter Faktor konnte nicht bestätigt werden',
    refusedAgain: 'Die Bestätigung der Identität hat nicht geklappt. Bitte erneut versuchen.',
  },
};
