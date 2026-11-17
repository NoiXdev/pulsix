import { describe, expect, it } from 'vitest';
import { placeAt, placeUnder } from './menuPlacement.ts';

const VIEW = { width: 1000, height: 800 };
const W = 200;
const H = 150;

describe('placeAt', () => {
  it('opens at the point when it fits', () => {
    expect(placeAt({ x: 300, y: 200 }, W, H, VIEW)).toEqual({ top: 200, left: 300 });
  });

  it('keeps clear of the right edge', () => {
    expect(placeAt({ x: 950, y: 200 }, W, H, VIEW)).toEqual({ top: 200, left: 1000 - W - 8 });
  });

  it('keeps clear of the left edge', () => {
    expect(placeAt({ x: 2, y: 200 }, W, H, VIEW).left).toBe(8);
  });

  it('opens upwards from the pointer near the bottom edge', () => {
    expect(placeAt({ x: 300, y: 750 }, W, H, VIEW)).toEqual({ top: 750 - H, left: 300 });
  });

  // A menu opened from the keyboard sits below its row (y = row bottom);
  // flipped, it ends at the row's top instead of covering the row.
  it('opens upwards above the row near the bottom edge', () => {
    expect(placeAt({ x: 300, y: 760, above: 720 }, W, H, VIEW)).toEqual({ top: 720 - H, left: 300 });
  });

  it('never goes above the top edge', () => {
    expect(placeAt({ x: 300, y: 700, above: 100 }, W, 400, VIEW).top).toBe(8);
  });
});

describe('placeUnder', () => {
  const trigger = (top: number, right = 500) => ({ top, bottom: top + 28, left: right - 28, right, width: 28, height: 28, x: right - 28, y: top, toJSON: () => ({}) }) as DOMRect;

  it('goes under the trigger, right-aligned to it', () => {
    expect(placeUnder(trigger(100), W, H, VIEW)).toEqual({ top: 132, left: 500 - W });
  });

  it('flips above the trigger near the bottom edge', () => {
    expect(placeUnder(trigger(700), W, H, VIEW)).toEqual({ top: 700 - 4 - H, left: 500 - W });
  });
});
