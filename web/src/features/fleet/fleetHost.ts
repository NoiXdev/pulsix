import type { Tone } from '../../ui/badge.tsx';
import type { FleetHost } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

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
 *
 * allIgnored (backlog 3.44) narrows that null case further: true means
 * check_host_states carries no row precisely because every item this host
 * discovered currently resolves to ignored, not because nothing was ever
 * measured. It is its own word ("all ignored"), not a shade of "no data" —
 * an operator who silenced the last item on a host must be able to tell
 * their own decision apart from a host nobody is watching or an agent that
 * went quiet, and a shared label would erase exactly that. `accent`
 * (badge.tsx's own non-state tone, already used for a deliberate,
 * non-alarming marker like "invited") keeps it visually apart from
 * `unknown`'s "no data" too, so the distinction does not rely on the label
 * text alone either.
 */
export function fleetStateLabel(state: FleetHost['state'], allIgnored: boolean, t: Messages): { label: string; tone: Tone } {
  const s = t.fleet.state;
  if (state === null) return allIgnored ? { label: s.allIgnored, tone: 'accent' } : { label: s.noData, tone: 'unknown' };
  switch (state) {
    case 'ok':
      return { label: s.ok, tone: 'ok' };
    case 'warn':
      return { label: s.warn, tone: 'warn' };
    case 'crit':
      return { label: s.crit, tone: 'crit' };
    default:
      return { label: s.unknown, tone: 'unknown' };
  }
}
