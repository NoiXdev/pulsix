import type { Tone } from '../../ui/badge.tsx';
import type { FleetHost } from '../../api/types.ts';

/**
 * The pure reading rules behind FleetPage, kept beside it rather than
 * inside it — the same split HostChecksCard's own hostChecks.ts draws, for
 * the same reasons: a file exporting a component may not also export plain
 * functions without breaking fast refresh, and this is a contract worth a
 * table-driven test of its own.
 */

/**
 * fleetStateLabel renders one host's roll-up state as a label and a Pill
 * tone, mirroring hostChecks.ts's own stateLabel for a single tuple — but
 * `null` here means something different, and the label says so.
 *
 * A tuple's null state (HostChecksCard) means that ONE reading was never
 * evaluated. A host's null state here means check_host_states carries no
 * row for this host AT ALL — evaluated to zero tuples, or never evaluated
 * — which the manager deletes the roll-up row for rather than leaving a
 * stale colour behind (fleet_handlers.go's own toFleetHostDTO). The
 * controller's own ruling is that this must read as "no data", never as a
 * colour the host happened to be showing when its last check vanished.
 */
export function fleetStateLabel(state: FleetHost['state']): { label: string; tone: Tone } {
  if (state === null) return { label: 'no data', tone: 'unknown' };
  switch (state) {
    case 'ok':
      return { label: 'ok', tone: 'ok' };
    case 'warn':
      return { label: 'warn', tone: 'warn' };
    case 'crit':
      return { label: 'crit', tone: 'crit' };
    default:
      return { label: 'unknown', tone: 'unknown' };
  }
}
