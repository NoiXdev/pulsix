/// <reference types="node" />
// The test reads the files from disk: vitest runs with `css: false`, so a CSS
// `?raw` import would arrive empty.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

describe('the app fonts', () => {
  it('are not loaded from a third party, which the manager CSP would block anyway', () => {
    expect(read('../../index.html')).not.toMatch(/fonts\.googleapis\.com|fonts\.gstatic\.com/);
  });

  it('are served from the app itself for both families the tokens name', () => {
    const fonts = read('./fonts.css');
    for (const family of ["'Inter'", "'JetBrains Mono'"]) {
      expect(fonts).toContain(`font-family: ${family}`);
    }
    expect(fonts).not.toMatch(/url\(\s*['"]?https?:/);
    expect(read('./globals.css')).toContain("@import './fonts.css'");
  });
});
