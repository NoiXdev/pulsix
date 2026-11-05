import { describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useOwnStepsRecord } from './ownSteps.ts';

describe('useOwnStepsRecord', () => {
  it('records the steps of the group it belongs to', () => {
    const { result } = renderHook(() => useOwnStepsRecord('g1'));
    act(() => result.current.record({ from: 3, to: 4 }));
    act(() => result.current.record({ from: 4, to: 5 }));
    expect(result.current.steps).toEqual([
      { from: 3, to: 4 },
      { from: 4, to: 5 },
    ]);
  });

  it('starts over with another group, and drops a late step of the one before', () => {
    const { result, rerender } = renderHook(({ id }) => useOwnStepsRecord(id), { initialProps: { id: 'g1' } });
    act(() => result.current.record({ from: 3, to: 4 }));
    const recordOfG1 = result.current.record;

    rerender({ id: 'c1' });
    expect(result.current.steps).toEqual([]);
    act(() => result.current.record({ from: 1, to: 2 }));

    // A save of g1 that was in flight lands after the page moved on: it
    // neither joins c1's record nor replaces it.
    act(() => recordOfG1({ from: 4, to: 5 }));
    expect(result.current.steps).toEqual([{ from: 1, to: 2 }]);
  });
});
