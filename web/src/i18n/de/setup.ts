import type { Messages } from '../types.ts';

export const deSetup: Messages['setup'] = {
  title: 'Diese Installation einrichten',
  intro: 'Läuft nur einmal. Danach entfällt diese Seite, und alles geschieht innerhalb der Organisation.',
  stepOf: (n: number, total: number) => `Schritt ${n} von ${total}`,
  steps: {
    admin: { label: 'Instanzadministrator', sub: 'E-Mail-Adresse und Passwort' },
    org: { label: 'Erste Organisation', sub: 'Name und URL-Slug' },
    smtp: { label: 'Ausgehende E-Mails', sub: 'optional, später nachholbar' },
    mfa: { label: 'Zweiter Faktor', sub: 'Authenticator-App oder Passkey' },
  },
  admin: {
    title: 'Instanzadministrator anlegen',
    name: 'Name',
    repeatPassword: 'Passwort wiederholen',
    token: 'Setup-Token',
    tokenHint: 'Steht beim Start im Log des Managers. Erneut anzeigen mit:',
    tokenMismatch:
      'Dieses Setup-Token passt nicht. Kopieren Sie es erneut aus dem Log des Managers oder führen Sie den Befehl unten aus.',
  },
  org: {
    title: 'Erste Organisation anlegen',
    name: 'Name der Organisation',
    slug: 'URL-Slug',
  },
  smtp: {
    intro: 'Optional, später nachholbar. Ohne konfiguriertes SMTP können keine Einladungs-E-Mails versendet werden.',
    configureNow: 'SMTP jetzt konfigurieren',
  },
  mfa: {
    title: 'Zweiten Faktor jetzt einrichten',
    intro:
      'Als Nächstes wird das Konto des Instanzadministrators angelegt. Direkt danach muss eine Authenticator-App oder ein Passkey hinzugefügt werden — jedes Konto braucht einen zweiten Faktor, bevor es etwas anderes tun kann.',
    creating: 'Wird angelegt…',
    create: 'Administrator anlegen und fortfahren',
  },
  failed: 'Einrichtung konnte nicht abgeschlossen werden',
};
