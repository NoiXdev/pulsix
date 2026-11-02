/** The fleet overview (FleetPage, FleetFilters). `state` is the roll-up
 * state's own wording, used by fleetHost.ts. */
export const enFleet = {
  title: 'Fleet',
  state: {
    allIgnored: 'all ignored',
    noData: 'no data',
    ok: 'ok',
    warn: 'warn',
    crit: 'crit',
    unknown: 'unknown',
  },
  noAccess: 'The fleet overview is visible to members of this organization.',
  totals: (crit: number, warn: number, unknown: number, ok: number, noData: number, allIgnored: number) =>
    `Totals for this filtered view, not the whole organization: ${crit} crit · ${warn} warn · ${unknown} unknown · ${ok} ok · ${noData} no data · ${allIgnored} all ignored`,
  columns: {
    state: 'State',
    host: 'Host',
    zone: 'Zone',
    crit: 'Crit',
    warn: 'Warn',
    unknown: 'Unknown',
    since: 'Since',
  },
  sinceTitle: (age: string) => `in this state since ${age}`,
  empty: 'No hosts match this view.',
  filters: {
    zone: 'Zone',
    allZones: 'All zones',
    group: 'Monitoring group',
    allGroups: 'All groups',
    tag: 'Tag',
    tagPlaceholder: 'key or key=value',
  },
};
