import type { NotifyChannel } from '../../api/types.ts';

export type Preset = NonNullable<NonNullable<NotifyChannel['webhook']>['preset']>;

/** The presets whose URL Pulsix builds (backlog 4.8): no URL field. */
export const BUILT_URL: ReadonlySet<Preset> = new Set<Preset>(['pushover', 'twilio', 'pagerduty']);

/** The secrets each preset takes, in the order the form shows them. */
export const SECRET_NAMES: Partial<Record<Preset, readonly SecretName[]>> = {
  pushover: ['token', 'user'],
  twilio: ['auth_token'],
  pagerduty: ['routing_key'],
};

export type SecretName = 'token' | 'user' | 'auth_token' | 'routing_key';

/** A preset's own settings as the form holds them. */
export interface PresetSettings {
  accountSid: string;
  from: string;
  to: string;
  region: 'us' | 'eu';
}

export function initialSettings(channel?: NotifyChannel): PresetSettings {
  const w = channel?.webhook;
  return { accountSid: w?.account_sid ?? '', from: w?.from ?? '', to: w?.to ?? '', region: w?.region === 'eu' ? 'eu' : 'us' };
}

/** settingsBody is what the API takes of the settings for preset. */
export function settingsBody(preset: Preset, s: PresetSettings) {
  if (preset === 'twilio') return { account_sid: s.accountSid.trim(), from: s.from.trim(), to: s.to.trim() };
  if (preset === 'pagerduty') return { region: s.region };
  return {};
}
