import { describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { followOwnSteps, useHeldRevision, useOwnStepsRecord, type RevisionStep } from './ownSteps.ts';

describe('useOwnStepsRecord', () => {
  it('records the steps of the group it belongs to', () => {
    const { result } = renderHook(({ rev }) => useOwnStepsRecord('g1', rev), { initialProps: { rev: 3 } });
    act(() => result.current.record({ from: 3, to: 4, kind: 'content' }));
    act(() => result.current.record({ from: 4, to: 5, kind: 'content' }));
    expect(result.current.steps).toEqual([
      { from: 3, to: 4, kind: 'content' },
      { from: 4, to: 5, kind: 'content' },
    ]);
  });

  it('starts over with another group, and drops a late step of the one before', () => {
    const { result, rerender } = renderHook(({ id, rev }) => useOwnStepsRecord(id, rev), { initialProps: { id: 'g1', rev: 3 } });
    act(() => result.current.record({ from: 3, to: 4, kind: 'content' }));
    const recordOfG1 = result.current.record;

    rerender({ id: 'c1', rev: 3 });
    expect(result.current.steps).toEqual([]);
    act(() => result.current.record({ from: 1, to: 2, kind: 'content' }));

    // A save of g1 that was in flight lands after the page moved on: it
    // neither joins c1's record nor replaces it.
    act(() => recordOfG1({ from: 4, to: 5, kind: 'content' }));
    expect(result.current.steps).toEqual([{ from: 1, to: 2, kind: 'content' }]);
  });

  it("drops on record the steps below the group's revision", () => {
    const { result, rerender } = renderHook(({ rev }) => useOwnStepsRecord('g1', rev), { initialProps: { rev: 3 } });
    act(() => result.current.record({ from: 3, to: 4, kind: 'content' }));
    rerender({ rev: 4 });
    act(() => result.current.record({ from: 4, to: 5, kind: 'content' }));
    rerender({ rev: 5 });
    act(() => result.current.record({ from: 5, to: 6, kind: 'content' }));
    expect(result.current.steps).toEqual([{ from: 5, to: 6, kind: 'content' }]);
  });

  it('keeps the steps from the shown revision while its refetch has not landed', () => {
    const { result } = renderHook(({ rev }) => useOwnStepsRecord('g1', rev), { initialProps: { rev: 3 } });
    act(() => result.current.record({ from: 3, to: 4, kind: 'content' }));
    act(() => result.current.record({ from: 4, to: 5, kind: 'content' }));
    expect(result.current.steps).toEqual([
      { from: 3, to: 4, kind: 'content' },
      { from: 4, to: 5, kind: 'content' },
    ]);
  });

  it('keeps a chain from a held revision however long it grows', () => {
    const { result, rerender } = renderHook(({ rev }) => useOwnStepsRecord('g1', rev), { initialProps: { rev: 3 } });
    let release: () => void = () => undefined;
    act(() => {
      release = result.current.hold(3);
    });
    for (let r = 3; r < 63; r++) {
      act(() => result.current.record({ from: r, to: r + 1, kind: 'content' }));
      rerender({ rev: r + 1 });
    }
    expect(result.current.steps).toHaveLength(60);
    expect(result.current.steps[0]).toEqual({ from: 3, to: 4, kind: 'content' });

    act(() => release());
    act(() => result.current.record({ from: 63, to: 64, kind: 'content' }));
    expect(result.current.steps).toEqual([{ from: 63, to: 64, kind: 'content' }]);
  });
});

describe('followOwnSteps', () => {
  const steps: RevisionStep[] = [
    { from: 3, to: 4, kind: 'content' },
    { from: 4, to: 5, kind: 'settings' },
    { from: 5, to: 6, kind: 'content' },
  ];

  it('walks every kind of step', () => {
    expect(followOwnSteps(3, steps)).toBe(6);
  });

  it('walks only the steps of the kind asked for', () => {
    expect(followOwnSteps(3, steps, 'content')).toBe(4);
  });
});

describe('useHeldRevision', () => {
  it('holds, re-holds and releases', () => {
    const releases: ReturnType<typeof vi.fn<() => void>>[] = [];
    const hold = vi.fn<(r: number) => () => void>(() => {
      const release = vi.fn<() => void>();
      releases.push(release);
      return release;
    });
    const { rerender, unmount } = renderHook(({ rev }: { rev: number | null }) => useHeldRevision(hold, rev), {
      initialProps: { rev: 3 as number | null },
    });
    expect(hold.mock.calls).toEqual([[3]]);

    rerender({ rev: 4 });
    expect(releases[0]).toHaveBeenCalledOnce();
    expect(hold.mock.calls).toEqual([[3], [4]]);
    expect(releases[0].mock.invocationCallOrder[0]).toBeLessThan(hold.mock.invocationCallOrder[1]);

    rerender({ rev: null });
    expect(releases[1]).toHaveBeenCalledOnce();
    expect(hold).toHaveBeenCalledTimes(2);

    unmount();
    expect(hold).toHaveBeenCalledTimes(2);
    expect(releases[0]).toHaveBeenCalledOnce();
    expect(releases[1]).toHaveBeenCalledOnce();
  });
});
