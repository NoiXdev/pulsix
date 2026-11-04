import type { Messages } from '../types.ts';

export const deUi: Messages['ui'] = {
  loadMore: 'Mehr laden',
  select: {
    open: 'Öffnen',
    noOptions: 'Keine Optionen',
    noMatch: 'Kein Treffer',
    unavailable: (value: string) => `${value} (nicht verfügbar)`,
  },
  query: {
    signingOutTitle: 'Abmeldung',
    signingOutDetail: 'Die Sitzung ist abgelaufen. Weiterleitung zur Anmeldung…',
    forbiddenTitle: 'Kein Zugriff',
    forbiddenDetail: 'Jemand mit der Rolle owner oder admin in der Organisation kann den Zugriff auf diese Seite freigeben.',
    notFoundTitle: 'Nicht gefunden',
    notFoundDetail: 'Diese Seite existiert nicht, oder es besteht kein Zugriff darauf.',
    failedTitle: 'Etwas ist schiefgelaufen',
    failedDetail: 'Ein unerwarteter Fehler ist aufgetreten. Bitte erneut versuchen.',
  },
  guard: {
    loadMoreFailed: 'Weitere Einträge konnten nicht geladen werden – angezeigt wird, was bereits geladen ist.',
    refreshFailed: 'Die Seite konnte nicht aktualisiert werden – angezeigt werden die zuletzt geladenen Daten.',
  },
  formPage: {
    actions: 'Formularaktionen',
    unsaved: 'Diese Seite verlassen? Ungespeicherte Änderungen gehen verloren.',
  },
  slug: {
    generateLabel: 'Slug aus dem Namen erzeugen',
    generateTitle: 'Aus dem Namen erzeugen',
  },
  chart: {
    ranges: {
      '1h': 'in der letzten Stunde',
      '24h': 'in den letzten 24 Stunden',
      '7d': 'in den letzten 7 Tagen',
    },
    label: (what: string, range: string, samples: number) => `${what}, ${range}, ${samples} Messwerte`,
    empty: 'Keine Messwerte in diesem Zeitraum.',
  },
};
