/**
 * intervalLabel writes a check's period the way an operator says it —
 * "1 min", "5 min", "1 h" — falling back to seconds for anything that is
 * not a whole number of them.
 *
 * It lives in its own module because two views render the same period:
 * the organization's definition table (ChecksPage) and the per-host
 * effective set (features/hosts/HostChecksCard). One check written "5 min"
 * on one page and "300 s" on the other would read as two different
 * configurations.
 */
export function intervalLabel(seconds: number): string {
  if (seconds > 0 && seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds > 0 && seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}
