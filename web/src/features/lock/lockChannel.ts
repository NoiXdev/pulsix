/**
 * The BroadcastChannel the tabs of one browser use to tell each other what
 * happened to the shared session (backlog 3.92, 3.93). The cookie is shared
 * by every tab, so a lock, an unlock or a renewal in one is true for all;
 * a message only lets the others follow at once instead of on their next
 * request. The server stays the authority: a tab that misses a message
 * still learns from the next 403 session_locked or /auth/me.
 */
export type SessionMessage = { type: 'locked' } | { type: 'unlocked' } | { type: 'renewed'; expiresAt: string };

const CHANNEL = 'pulsix-session';

function isSessionMessage(data: unknown): data is SessionMessage {
  if (!data || typeof data !== 'object' || !('type' in data)) return false;
  if (data.type === 'locked' || data.type === 'unlocked') return true;
  return data.type === 'renewed' && 'expiresAt' in data && typeof data.expiresAt === 'string';
}

function open(): BroadcastChannel | null {
  return typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(CHANNEL);
}

/** postSessionMessage tells the other tabs. A channel never delivers to the
 * instance that posted, but each call opens its own instance, so a listener
 * registered by onSessionMessage in the SAME tab does hear it: a receiver
 * must tolerate news about what it just did itself. */
export function postSessionMessage(msg: SessionMessage): void {
  const channel = open();
  if (!channel) return;
  channel.postMessage(msg);
  channel.close();
}

/** onSessionMessage calls `cb` with every message another tab posts.
 * Returns the function that stops listening. */
export function onSessionMessage(cb: (msg: SessionMessage) => void): () => void {
  const channel = open();
  if (!channel) return () => {};
  channel.onmessage = (e: MessageEvent) => {
    if (isSessionMessage(e.data)) cb(e.data);
  };
  return () => channel.close();
}
