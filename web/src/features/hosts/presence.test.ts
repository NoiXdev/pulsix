import { describe, expect, it } from 'vitest';
import { hostPresence } from './presence.ts';

const online = { status: 'online' as const, last_seen: '2026-11-13T10:00:00Z' };
const offline = { status: 'offline' as const, last_seen: '2026-11-12T18:00:00Z' };

describe('hostPresence', () => {
  // Backlog 3.57: "off" is a client whose agent is offline — the host the
  // manager freezes. A server whose agent is offline is not off: it is a
  // server that stopped reporting, and its roll-up says so. A client
  // without an agent is treated like a server (nothing says it is off).
  it.each([
    ['a server with an online agent', 'server', online, { kind: 'server' }],
    ['a server with an offline agent', 'server', offline, { kind: 'server' }],
    ['a server without an agent', 'server', undefined, { kind: 'server' }],
    ['a client with an online agent', 'client', online, { kind: 'client' }],
    ['a client without an agent', 'client', undefined, { kind: 'client' }],
    ['a client without an agent (null)', 'client', null, { kind: 'client' }],
    ['a client with an offline agent', 'client', offline, { kind: 'off', lastSeen: '2026-11-12T18:00:00Z' }],
    ['a client whose offline agent never reported', 'client', { status: 'offline' as const }, { kind: 'off', lastSeen: undefined }],
    ['a host whose kind is not known yet', undefined, offline, { kind: 'server' }],
  ] as const)('%s', (_name, kind, agent, expected) => {
    expect(hostPresence(kind, agent)).toEqual(expected);
  });
});
