import type { Messages } from '../types.ts';

export const deVersion: Messages['version'] = {
  title: (v) => `Pulsix wurde auf v${v} aktualisiert`,
  intro: 'Lade die Seite neu, um die neue Version zu nutzen. Bis dahin kann es sein, dass Anzeigen nicht mehr zum Server passen.',
  releaseNotes: 'Versionshinweise',
  reload: 'Neu laden',
  later: 'Später',
  bannerText: (v) => `Pulsix wurde auf v${v} aktualisiert.`,
};
