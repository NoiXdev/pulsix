/** Whether a token's expiry (an ISO time, absent for never) has passed. */
export function isExpired(expiresAt?: string): boolean {
  return !!expiresAt && new Date(expiresAt).getTime() <= Date.now();
}
