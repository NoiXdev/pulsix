/** The organization's activity feed (ActivityPage). */
export const enActivity = {
  title: 'Activity',
  subtitle: 'Every change in this organization, by people, tokens and Pulsix itself',
  filterByActor: 'Filter by actor',
  anyActor: 'Actor: anyone',
  allTypes: 'all',
  empty: 'No activity recorded yet.',
  user: (id: string) => `user · ${id}`,
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
