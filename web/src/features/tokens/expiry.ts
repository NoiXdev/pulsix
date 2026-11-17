/** Whether a token's expiry (an ISO time, absent for never) has passed. */
export function isExpired(expiresAt?: string): boolean {
  return !!expiresAt && new Date(expiresAt).getTime() <= Date.now();
}

/** The local calendar day of an instant as YYYY-MM-DD, for a date input. */
export function localDay(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The last millisecond of a YYYY-MM-DD day in local time, as an ISO instant. */
export function endOfLocalDay(day: string): string {
  const [y, m, d] = day.split('-').map(Number);
  return new Date(y, m - 1, d, 23, 59, 59, 999).toISOString();
}
