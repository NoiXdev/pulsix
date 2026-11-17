import { describe, expect, it, vi } from 'vitest';
import { onSessionMessage, postSessionMessage, type SessionMessage } from './lockChannel.ts';

describe('lockChannel', () => {
  it('delivers a message another tab posts on the pulsix-session channel', async () => {
    const got = vi.fn();
    const stop = onSessionMessage(got);
    try {
      const other = new BroadcastChannel('pulsix-session');
      other.postMessage({ type: 'locked' });
      other.close();
      await vi.waitFor(() => expect(got).toHaveBeenCalledWith({ type: 'locked' }));
    } finally {
      stop();
    }
  });

  it('round-trips the three message types and ignores junk', async () => {
    const got = vi.fn();
    const stop = onSessionMessage(got);
    try {
      const msgs: SessionMessage[] = [{ type: 'locked' }, { type: 'unlocked' }, { type: 'renewed', expiresAt: '2030-01-01T00:00:00Z' }];
      for (const m of msgs) postSessionMessage(m);
      const other = new BroadcastChannel('pulsix-session');
      other.postMessage({ type: 'bogus' });
      other.postMessage('nope');
      other.close();
      await vi.waitFor(() => expect(got).toHaveBeenCalledTimes(3));
      expect(got.mock.calls.map((c) => c[0])).toEqual(msgs);
    } finally {
      stop();
    }
  });

  it('stops delivering after the listener is removed', async () => {
    const got = vi.fn();
    onSessionMessage(got)();
    postSessionMessage({ type: 'locked' });
    await new Promise((r) => setTimeout(r, 30));
    expect(got).not.toHaveBeenCalled();
  });
});
