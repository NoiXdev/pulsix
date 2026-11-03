import type { Format } from '../../i18n/format.ts';

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

/** A size in the largest binary unit that keeps it at or above 1, with the
 * active language's number format: "412 MB", "1,4 MB". */
export function formatSize(f: Format, bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${f.number(value, { maximumFractionDigits: value >= 100 || unit === 0 ? 0 : 1 })} ${UNITS[unit]}`;
}
