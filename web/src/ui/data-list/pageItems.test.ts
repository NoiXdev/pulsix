import { describe, expect, it } from 'vitest';
import { pageItems } from './pageItems.ts';

describe('pageItems', () => {
  it.each([
    [1, 1, [1]],
    [2, 4, [1, 2, 3, 4]],
    [5, 10, [1, 'gap', 4, 5, 6, 'gap', 10]],
    [3, 10, [1, 2, 3, 4, 'gap', 10]],
    // The gap before 3 would hide only page 2, so 2 is shown instead.
    [4, 10, [1, 2, 3, 4, 5, 'gap', 10]],
    [10, 10, [1, 'gap', 9, 10]],
    [1, 10, [1, 2, 'gap', 10]],
    [1, 2, [1, 2]],
  ] as const)('page %i of %i', (page, count, want) => {
    expect(pageItems(page, count)).toEqual(want);
  });

  it('treats a page count below one as one page', () => {
    expect(pageItems(1, 0)).toEqual([1]);
  });
});
