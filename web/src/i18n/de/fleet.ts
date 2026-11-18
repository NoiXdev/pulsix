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
    `Summen für diese gefilterte Ansicht, nicht für die ganze Organisation: kritisch: ${crit} · Warnung: ${warn} · unbekannt: ${unknown} · ok: ${ok} · keine Daten: ${noData} · alle ignoriert: ${allIgnored}`,
  label: 'Fleet-Hosts',
  count: (n: number) => (n === 1 ? '1 Host' : `${n} Hosts`),
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
    search: 'Hosts durchsuchen',
    searchPlaceholder: 'Hosts filtern …',
    state: 'Nach Zustand filtern',
    anyState: 'Beliebiger Zustand',
    zone: 'Nach Zone filtern',
    allZones: 'Alle Zonen',
    group: 'Nach Überwachungsgruppe filtern',
    allGroups: 'Alle Gruppen',
    tag: 'Nach Tag filtern',
    tagPlaceholder: 'Schlüssel oder Schlüssel=Wert',
  },
};
