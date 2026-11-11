import { describe, expect, it, vi } from 'vitest';
import { renderHook } from '@testing-library/react';

const back = vi.fn();
vi.mock('@tanstack/react-router', () => ({ useRouter: () => ({ history: { back } }) }));

import { useLeaveQuestion } from './useLeaveQuestion.ts';

function deferred() {
  let resolve!: (v: boolean) => void;
  const promise = new Promise<boolean>((r) => (resolve = r));
  return { promise, resolve };
}

describe('useLeaveQuestion', () => {
  it('blocks when the user stays and lets the navigation through when they leave', async () => {
    const { result } = renderHook(() => useLeaveQuestion(() => Promise.resolve(false)));
    expect(await result.current({ action: 'PUSH' })).toBe(true);
    const leaving = renderHook(() => useLeaveQuestion(() => Promise.resolve(true)));
    expect(await leaving.result.current({ action: 'BACK' })).toBe(false);
  });

  it('blocks a second navigation at once and does not ask again', async () => {
    const d = deferred();
    const ask = vi.fn(() => d.promise);
    const { result } = renderHook(() => useLeaveQuestion(ask));
    const first = result.current({ action: 'PUSH' });
    expect(await result.current({ action: 'PUSH' })).toBe(true);
    d.resolve(false);
    expect(await first).toBe(true);
    expect(ask).toHaveBeenCalledTimes(1);
  });

  it('lets a Back through that a second pop superseded, so its own undo cannot over-correct', async () => {
    back.mockClear();
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    const first = result.current({ action: 'BACK' });
    expect(await result.current({ action: 'BACK' })).toBe(true);
    d.resolve(false);
    expect(await first).toBe(false);
    expect(back).not.toHaveBeenCalled();
  });

  it('goes Back itself when a superseded Back is confirmed', async () => {
    back.mockClear();
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    const first = result.current({ action: 'BACK' });
    await result.current({ action: 'BACK' });
    d.resolve(true);
    expect(await first).toBe(false);
    expect(back).toHaveBeenCalledWith({ ignoreBlocker: true });
  });

  it('keeps the normal answer when the second navigation is a push, not a pop', async () => {
    back.mockClear();
    const d = deferred();
    const { result } = renderHook(() => useLeaveQuestion(() => d.promise));
    const first = result.current({ action: 'BACK' });
    await result.current({ action: 'PUSH' });
    d.resolve(false);
    expect(await first).toBe(true);
    expect(back).not.toHaveBeenCalled();
  });
});
