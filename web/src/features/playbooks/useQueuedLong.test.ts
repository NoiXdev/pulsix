import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QUEUED_HINT_MS, useQueuedLong } from './useQueuedLong.ts';

describe('useQueuedLong', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-22T10:00:00Z'));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('turns true once the job has been queued for the hint delay', () => {
    const { result } = renderHook(() => useQueuedLong(true, '2026-11-22T10:00:00Z'));
    expect(result.current).toBe(false);
    act(() => void vi.advanceTimersByTime(QUEUED_HINT_MS + 1));
    expect(result.current).toBe(true);
  });

  it('stays false while the job is not queued', () => {
    const { result } = renderHook(() => useQueuedLong(false, '2026-11-22T09:00:00Z'));
    expect(result.current).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('starts no timer for a created_at that does not parse', () => {
    const { result } = renderHook(() => useQueuedLong(true, 'not a time'));
    expect(vi.getTimerCount()).toBe(0);
    act(() => void vi.advanceTimersByTime(60_000));
    expect(result.current).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });
});
