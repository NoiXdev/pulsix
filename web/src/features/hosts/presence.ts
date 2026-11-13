import type { Agent, Host } from '../../api/types.ts';

/**
 * HostPresence is what the hosts list, the fleet overview and the host page
 * say about whether a host is expected to be on (backlog 3.57):
 *
 *  - `server`: expected to be on; shown as before.
 *  - `client`: a client that is on, or one without an agent — nothing
 *    tells Pulsix such a client is off, so it is judged like a server.
 *    Shown like a server plus a small "Client" tag.
 *  - `off`: a client whose agent is offline. The manager freezes it (no
 *    unknown, no host alert), so its last state is not current; it is
 *    shown as "Off" with when its agent was last seen.
 */
export type HostPresence = { kind: 'server' } | { kind: 'client' } | { kind: 'off'; lastSeen: string | undefined };

/** The part of an agent presence reads: the same status the agents list
 * and the host's agent card show (online = a recent heartbeat). */
export type AgentPresence = Pick<Agent, 'status' | 'last_seen'>;

/**
 * hostPresence decides a host's presence from its kind and its agent, if
 * it has one. `kind` is undefined while unknown, which reads as a server:
 * nothing is marked off on a guess.
 */
export function hostPresence(kind: Host['kind'] | undefined, agent: AgentPresence | null | undefined): HostPresence {
  if (kind !== 'client') return { kind: 'server' };
  if (agent && agent.status !== 'online') return { kind: 'off', lastSeen: agent.last_seen };
  return { kind: 'client' };
}
