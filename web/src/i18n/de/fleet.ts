import type { Messages } from '../types.ts';

export const deFleet: Messages['fleet'] = {
  title: 'Fleet',
  state: {
    allIgnored: 'alle ignoriert',
    noData: 'keine Daten',
    ok: 'ok',
    warn: 'Warnung',
    crit: 'kritisch',
    unknown: 'unbekannt',
  },
  noAccess: 'Die Fleet-Übersicht ist für Mitglieder dieser Organisation sichtbar.',
  totals: (crit: number, warn: number, unknown: number, ok: number, noData: number, allIgnored: number) =>
    `Summen für diese gefilterte Ansicht, nicht für die ganze Organisation: ${crit} kritisch · ${warn} Warnung · ${unknown} unbekannt · ${ok} ok · ${noData} keine Daten · ${allIgnored} alle ignoriert`,
  columns: {
    state: 'Zustand',
    host: 'Host',
    zone: 'Zone',
    crit: 'Kritisch',
    warn: 'Warnung',
    unknown: 'Unbekannt',
    since: 'Seit',
  },
  sinceTitle: (age: string) => `Zustand eingetreten ${age}`,
  empty: 'Keine Hosts in dieser Ansicht.',
  filters: {
    zone: 'Zone',
    allZones: 'Alle Zonen',
    group: 'Überwachungsgruppe',
    allGroups: 'Alle Gruppen',
    tag: 'Tag',
    tagPlaceholder: 'Schlüssel oder Schlüssel=Wert',
  },
};
