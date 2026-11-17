import { afterEach, describe, expect, it, vi } from 'vitest';
import { applyDensityHint, readDensityHint, writeDensityHint } from './densityHint.ts';

afterEach(() => {
  vi.restoreAllMocks();
  window.localStorage.clear();
  delete document.documentElement.dataset.density;
});

describe('the density hint', () => {
  it('reads back what was written', () => {
    writeDensityHint('compact');
    expect(readDensityHint()).toBe('compact');
  });

  it('ignores a value that is not a density', () => {
    window.localStorage.setItem('pulsix.density', 'dense');
    expect(readDensityHint()).toBeUndefined();
  });

  it('works without storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    expect(() => writeDensityHint('compact')).not.toThrow();
    expect(readDensityHint()).toBeUndefined();
    expect(() => applyDensityHint()).not.toThrow();
    expect(document.documentElement).not.toHaveAttribute('data-density');
  });

  it('is put on the document at startup', () => {
    writeDensityHint('comfortable');
    applyDensityHint();
    expect(document.documentElement).toHaveAttribute('data-density', 'comfortable');
  });

  it('leaves the document alone without a hint', () => {
    applyDensityHint();
    expect(document.documentElement).not.toHaveAttribute('data-density');
  });
});
