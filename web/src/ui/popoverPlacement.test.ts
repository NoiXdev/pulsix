import { describe, expect, it } from 'vitest';
import { placePopover } from './popoverPlacement.ts';

const VIEW = { width: 1000, height: 800 };
const POP = { width: 260, height: 320 };
const field = (top: number, left = 100) => ({ top, bottom: top + 32, left, width: 200, height: 32 });

describe('placePopover', () => {
  it('goes below the field when it fits there', () => {
    expect(placePopover(field(100), POP, VIEW)).toEqual({ top: 136, left: 100, side: 'below' });
  });

  it('goes above the field when there is no room below', () => {
    expect(placePopover(field(700), POP, VIEW)).toEqual({ top: 700 - 4 - 320, left: 100, side: 'above' });
  });

  it('stays below when neither side fits and below has more room', () => {
    expect(placePopover(field(300), { width: 260, height: 700 }, VIEW).side).toBe('below');
  });

  it('never runs off the top of the viewport when placed above', () => {
    expect(placePopover(field(400), { width: 260, height: 600 }, { width: 1000, height: 450 }).top).toBe(8);
  });

  it('is pulled back inside the right edge', () => {
    expect(placePopover(field(100, 900), POP, VIEW).left).toBe(1000 - 260 - 8);
  });

  it('gives a finite answer in a world with no layout', () => {
    const zero = { top: 0, bottom: 0, left: 0, width: 0, height: 0 };
    expect(placePopover(zero, { width: 0, height: 0 }, { width: 0, height: 0 })).toEqual({ top: 4, left: 8, side: 'below' });
  });
});
