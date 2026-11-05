import { describe, expect, it } from 'vitest';
import { clippedElements, isClipped } from './clipped.ts';

describe('isClipped', () => {
  it.each([
    { classes: 'overflow-hidden h-40', want: true },
    { classes: 'overflow-clip max-h-[300px]', want: true },
    { classes: 'overflow-y-hidden h-64', want: true },
    { classes: 'md:overflow-y-hidden lg:max-h-96', want: true },
    { classes: 'overflow-hidden h-px', want: true },
    { classes: 'overflow-hidden h-screen', want: true },
    { classes: 'overflow-hidden max-h-screen', want: true },
    { classes: 'overflow-hidden max-h-dvh', want: true },
    { classes: 'overflow-hidden max-h-full', want: true },
    { classes: 'overflow-y-auto max-h-48', want: false },
    { classes: 'overflow-hidden rounded-md', want: false },
    { classes: 'overflow-hidden h-full', want: false },
    { classes: 'h-8 w-8', want: false },
  ])('reads "$classes" as clipped: $want', ({ classes, want }) => {
    expect(isClipped(classes.split(' '))).toBe(want);
  });
});

describe('clippedElements', () => {
  function html(markup: string): HTMLElement {
    const root = document.createElement('div');
    root.innerHTML = markup;
    return root;
  }

  it('finds elements clipped by class or by inline style, and only those', () => {
    const root = html(`
      <div id="a" class="overflow-hidden h-40"></div>
      <div id="b" style="overflow: hidden; max-height: 120px"></div>
      <div id="c" style="overflow-y: clip; height: 10px"></div>
      <div id="d" class="overflow-auto max-h-48"></div>
      <div id="e" style="overflow: hidden"></div>
      <div id="f" style="height: 10px"></div>
    `);
    expect(clippedElements(root).map((el) => el.id)).toEqual(['a', 'b', 'c']);
  });

  it('returns nothing for a tree that clips nothing', () => {
    expect(clippedElements(html('<div class="overflow-y-auto max-h-48"><p class="h-8"></p></div>'))).toEqual([]);
  });
});
