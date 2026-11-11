import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderHook } from '@testing-library/react';

const go = vi.fn();
const location = { state: {} as Record<string, unknown> };
vi.mock('@tanstack/react-router', () => ({ useRouter: () => ({ history: { go, location } }) }));

import { useLeaveQuestion } from './useLeaveQuestion.ts';

function deferred() {
  let resolve!: (v: boolean) => void;
  const promise = new Promise<boolean>((r) => (resolve = r));
  return { promise, resolve };
}

/** Puts the router on entry `router` and the browser on entry `browser`, as
 * they stand when a popstate reaches the blocker (the browser has moved,
 * the router not yet). */
function at(router: number, browser: number) {
  location.state = { __TSR_index: router };
  window.history.replaceState({ __TSR_index: browser }, '');
}

beforeEach(() => {
  go.mockClear();
  at(2, 2);
});

afterEach(() => {
  window.history.replaceState(null, '');
});

describe('useLeaveQuestion', () => {
  it('blocks when the user stays and lets the navigation through when they leave', async () => {
    const { result } = renderHook(() => useLeaveQuestion(() => Promise.resolve(false)));
    expect(await result.current.shouldBlock({ action: 'PUSH' })).toBe(true);
    const leaving = renderHook(() => useLeaveQuestion(() => Promise.resolve(true)));
    at(2, 1);
    expect(await leaving.result.current.shouldBlock({ action: 'BACK' })).toBe(false);
  });

  it('blocks a second navigation at once and does not ask again', async () => {
    const d = deferred();
    const ask = vi.fn(() => d.promise);
    const { result } = renderHook(() => useLeaveQuestion(ask));
    const first = result.current.shouldBlock({ action: 'PUSH' });
    expect(await result.current.shouldBlock({ action: 'PUSH' })).toBe(true);
    d.resolve(false);
    expect(await first).toBe(true);
    expect(ask).toHaveBeenCalledTimes(1);
  });

  it('lets a Back through that a second pop superseded, so its own undo cannot over-correct', async () => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    at(2, 0);
    expect(await result.current.shouldBlock({ action: 'GO' })).toBe(true);
    d.resolve(false);
    expect(await first).toBe(false);
    expect(go).not.toHaveBeenCalled();
  });

  it('goes Back itself, by the captured distance, when a superseded Back is confirmed', async () => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    at(2, 0);
    await result.current.shouldBlock({ action: 'GO' });
    at(2, 2);
    d.resolve(true);
    expect(await first).toBe(false);
    expect(go).toHaveBeenCalledWith(-1, { ignoreBlocker: true });
  });

  it.each([
    { action: 'FORWARD' as const, browser: 3, delta: 1 },
    { action: 'GO' as const, browser: 0, delta: -2 },
  ])('makes a superseded $action that is confirmed go the same distance ($delta)', async ({ action, browser, delta }) => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, browser);
    const first = result.current.shouldBlock({ action });
    at(2, 1);
    expect(await result.current.shouldBlock({ action: 'BACK' })).toBe(true);
    d.resolve(true);
    expect(await first).toBe(false);
    expect(go).toHaveBeenCalledWith(delta, { ignoreBlocker: true });
  });

  it('keeps the normal answer when the second navigation is a push, not a pop', async () => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    at(2, 2);
    await result.current.shouldBlock({ action: 'PUSH' });
    d.resolve(false);
    expect(await first).toBe(true);
    expect(go).not.toHaveBeenCalled();
  });

  it('never blocks a pop back onto the entry the router is on, which would reload the page', async () => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    // Forward while the question is open: the browser is back on the page.
    at(2, 2);
    expect(await result.current.shouldBlock({ action: 'GO' })).toBe(false);
    d.resolve(false);
    // Cancel: nothing left to undo, the router just stays.
    expect(await first).toBe(false);
    expect(go).not.toHaveBeenCalled();
  });

  it('goes Back itself when a Back superseded by a Forward is confirmed', async () => {
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    at(2, 2);
    await result.current.shouldBlock({ action: 'GO' });
    d.resolve(true);
    expect(await first).toBe(false);
    expect(go).toHaveBeenCalledWith(-1, { ignoreBlocker: true });
  });

  it('does not ask about a pop onto the current entry with no question open', async () => {
    const ask = vi.fn(() => Promise.resolve(false));
    const { result } = renderHook(() => useLeaveQuestion(ask));
    expect(await result.current.shouldBlock({ action: 'GO' })).toBe(false);
    expect(ask).not.toHaveBeenCalled();
  });

  it('asks again after a question that a second pop superseded', async () => {
    const d = deferred();
    const ask = vi.fn(() => d.promise);
    const { result } = renderHook(() => useLeaveQuestion(ask));
    at(2, 1);
    const first = result.current.shouldBlock({ action: 'BACK' });
    at(2, 2);
    await result.current.shouldBlock({ action: 'GO' });
    d.resolve(false);
    await first;
    at(2, 1);
    void result.current.shouldBlock({ action: 'BACK' });
    expect(ask).toHaveBeenCalledTimes(2);
  });

  describe('dismiss', () => {
    /** An ask that, like the confirm dialog, resolves false once withdrawn. */
    function withdrawable() {
      let signal: AbortSignal | undefined;
      const ask = vi.fn((s: AbortSignal) => {
        signal = s;
        return new Promise<boolean>((resolve) => s.addEventListener('abort', () => resolve(false)));
      });
      return { ask, signal: () => signal };
    }

    it('withdraws an open pop question and lets the pop through without an undo', async () => {
      const w = withdrawable();
      const { result } = renderHook(() => useLeaveQuestion(w.ask));
      at(2, 1);
      const first = result.current.shouldBlock({ action: 'BACK' });
      result.current.dismiss();
      expect(w.signal()?.aborted).toBe(true);
      expect(await first).toBe(false);
      expect(go).not.toHaveBeenCalled();
    });

    it('withdraws an open push question and drops the push', async () => {
      const w = withdrawable();
      const { result } = renderHook(() => useLeaveQuestion(w.ask));
      const first = result.current.shouldBlock({ action: 'PUSH' });
      result.current.dismiss();
      expect(await first).toBe(true);
    });

    it('withdraws a superseded pop without moving the browser itself', async () => {
      const w = withdrawable();
      const { result } = renderHook(() => useLeaveQuestion(w.ask));
      at(2, 1);
      const first = result.current.shouldBlock({ action: 'BACK' });
      at(2, 0);
      await result.current.shouldBlock({ action: 'GO' });
      result.current.dismiss();
      expect(await first).toBe(false);
      expect(go).not.toHaveBeenCalled();
    });

    it('does nothing without an open question', () => {
      const w = withdrawable();
      const { result } = renderHook(() => useLeaveQuestion(w.ask));
      expect(() => result.current.dismiss()).not.toThrow();
      expect(w.ask).not.toHaveBeenCalled();
    });
  });
});
