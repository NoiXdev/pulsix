import type { BackupSettings, BackupSettingsInput } from '../../api/types.ts';

/** The destination card's fields. */
export interface DestinationDraft {
  destination: 'folder' | 's3';
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  accessKey: string;
  /** What was typed into the secret field; empty means "leave it alone". */
  secretKey: string;
  removeSecret: boolean;
  pathStyle: boolean;
}

/** The schedule-and-retention card's fields. */
export interface ScheduleDraft {
  enabled: boolean;
  timeOfDay: string;
  timeZone: string;
  daily: string;
  weekly: string;
  monthly: string;
  includeMetrics: boolean;
}

/** The browser's own zone, offered while nothing has been saved. */
function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function toDestinationDraft(s: BackupSettings): DestinationDraft {
  return {
    destination: s.destination,
    endpoint: s.s3.endpoint,
    region: s.s3.region,
    bucket: s.s3.bucket,
    prefix: s.s3.prefix,
    accessKey: s.s3.access_key,
    secretKey: '',
    removeSecret: false,
    pathStyle: s.s3.path_style,
  };
}

/**
 * The schedule as stored. `forForm` offers the browser's zone instead of
 * the API's UTC for an instance that has never saved a schedule: an
 * administrator who has not chosen a zone yet most likely means their own.
 * A request that only carries the schedule along (saving the destination)
 * sends the stored value, not that suggestion.
 */
export function toScheduleDraft(s: BackupSettings, forForm = false): ScheduleDraft {
  return {
    enabled: s.enabled,
    timeOfDay: s.time_of_day,
    timeZone: forForm && !s.enabled && s.time_zone === 'UTC' ? browserZone() : s.time_zone,
    daily: String(s.retention.daily),
    weekly: String(s.retention.weekly),
    monthly: String(s.retention.monthly),
    includeMetrics: s.include_metrics,
  };
}

/**
 * The request body for one card's draft on top of the other card's last
 * saved values — the API takes the whole settings object. `secret_key` is
 * write-only: absent keeps the stored one (the normal case, since the page
 * cannot show it back), a typed value replaces it, and only the explicit
 * switch sends "" to remove it.
 */
export function toBody(d: DestinationDraft, sch: ScheduleDraft): BackupSettingsInput {
  const s3: NonNullable<BackupSettingsInput['s3']> = {
    endpoint: d.endpoint.trim(),
    region: d.region.trim(),
    bucket: d.bucket.trim(),
    prefix: d.prefix.trim(),
    access_key: d.accessKey.trim(),
    path_style: d.pathStyle,
  };
  if (d.secretKey) s3.secret_key = d.secretKey;
  else if (d.removeSecret) s3.secret_key = '';
  return {
    destination: d.destination,
    s3,
    enabled: sch.enabled,
    time_of_day: sch.timeOfDay.trim(),
    time_zone: sch.timeZone.trim(),
    retention: { daily: Number(sch.daily), weekly: Number(sch.weekly), monthly: Number(sch.monthly) },
    include_metrics: sch.includeMetrics,
  };
}
