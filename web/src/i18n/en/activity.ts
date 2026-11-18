/** The organization's activity feed (ActivityPage). */
export const enActivity = {
  title: 'Activity',
  subtitle: 'Every change in this organization, by people, tokens and Pulsix itself',
  filterByActor: 'Filter by actor',
  anyActor: 'Actor: anyone',
  filterBySince: 'Filter by time',
  anyTime: 'Any time',
  since: { '1d': 'Last 24 hours', '7d': 'Last 7 days', '30d': 'Last 30 days' },
  noMatch: 'No activity matches the filters.',
  allTypes: 'all',
  empty: 'No activity recorded yet.',
  /** The organization's feed as a list. */
  list: {
    label: 'Activity',
    count: (n: number) => (n === 1 ? '1 event' : `${n} events`),
    columns: { at: 'When', actor: 'Who', action: 'What happened', object: 'Object' },
  },
  /** Settings > My activity: what the signed-in person did, in every organization. */
  my: {
    title: 'My activity',
    subtitle: 'What you did, in every organization you belong to.',
    label: 'My activity',
    at: 'When',
    organization: 'Organization',
    action: 'What happened',
    object: 'Object',
    count: (n: number) => (n === 1 ? '1 event' : `${n} events`),
    empty: 'You have not done anything yet.',
  },
  user: (id: string) => `user · ${id}`,
  /** The API token a change was made with. */
  viaToken: (name: string) => `via token ${name}`,
  /** Who made a change, by the kind of actor the API reports. */
  actors: {
    token: 'API token',
    system: 'Pulsix',
    schedule: 'Schedule',
    webhook: 'Webhook',
  },
  /** The object types an event can be about (also the filter chips). */
  objectTypes: {
    host: 'host',
    member: 'member',
    token: 'token',
    zone: 'zone',
    organization: 'organization',
    setup: 'setup',
    totp: 'totp',
    passkey: 'passkey',
    invite: 'invite',
    job_template: 'job_template',
    job: 'job',
    schedule: 'schedule',
    trigger: 'trigger',
    worker: 'worker',
    channel: 'channel',
    maintenance: 'maintenance',
  },
};
