import { useAlerts } from '../../api/hooks/useAlerts.ts';
import { summarizeAlerts, type AlertSummary } from './hostOverview.ts';

/**
 * useHostAlertSummary counts the host's open alerts for the page's pill,
 * its section nav and the "still open" line. It reads exactly what
 * HostAlertsCard reads (`host=<id>&state=open`, the same filter object and
 * so the same query), so the page asks once for both. Undefined until the
 * answer is there, or when the caller may not read checks.
 *
 * One page of open alerts (50) is what is counted; a host with more open
 * at once has a problem no exact count changes.
 */
export function useHostAlertSummary(org: string, hostId: string, canRead: boolean): AlertSummary | undefined {
  const alerts = useAlerts(org, { host: hostId, state: 'open' }, canRead);
  if (!canRead || !alerts.data) return undefined;
  return summarizeAlerts(alerts.data.pages.flatMap((p) => p.alerts ?? []));
}
