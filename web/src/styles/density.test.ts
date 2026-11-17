/// <reference types="node" />
// Reads the stylesheet from disk: vitest runs with `css: false`.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { ROW_GRID } from '../ui/data-list/DataRow.tsx';
import { CELL_MINOR } from '../ui/data-list/cellText.ts';

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

/** The `--px-row-font` the density block of `selector` sets, in px (the
 * stylesheet has other `:root` blocks; the density one sets `--px-row-py`). */
function rowFont(selector: string): number {
  const css = read('./globals.css');
  const blocks = css.split(`${selector} {`).slice(1).map((rest) => rest.slice(0, rest.indexOf('}')));
  const block = blocks.find((b) => b.includes('--px-row-py'));
  expect(block, `${selector} density block`).toBeDefined();
  const m = /--px-row-font:\s*(\d+(?:\.\d+)?)px/.exec(block!);
  expect(m, `${selector} sets --px-row-font`).not.toBeNull();
  return Number(m![1]);
}

describe('list density type sizes', () => {
  it("keep today's 12px (text-xs) for standard and before the preferences load", () => {
    expect(rowFont(':root')).toBe(12);
    expect(rowFont(":root[data-density='standard']")).toBe(12);
  });

  it('step down for compact and up for comfortable', () => {
    const compact = rowFont(":root[data-density='compact']");
    const comfortable = rowFont(":root[data-density='comfortable']");
    expect(compact).toBeLessThan(12);
    expect(comfortable).toBeGreaterThan(12);
  });

  it('are what a row reads, and secondary cell text is relative to it', () => {
    expect(ROW_GRID).toContain('text-[length:var(--px-row-font)]');
    expect(CELL_MINOR).toMatch(/^text-\[0\.\d+em\]$/);
  });
});
