import { describe, expect, it } from 'vitest';
import { validateListSearch } from './listSearch.ts';

describe('validateListSearch', () => {
  it('leaves page 1 out', () => {
    expect(validateListSearch({ page: '1' })).toEqual({});
    expect(validateListSearch({ page: 1 })).toEqual({});
  });

  it('keeps a page above 1 and drops an unknown order', () => {
    const out = validateListSearch({ page: '3', order: 'up' });
    expect(out).toEqual({ page: 3 });
    expect(Object.keys(out)).toEqual(['page']);
  });

  it('keeps a sort, an order and a page', () => {
    expect(validateListSearch({ sort: 'name', order: 'desc', page: 2 })).toEqual({ sort: 'name', order: 'desc', page: 2 });
  });

  it('drops what is not a page number', () => {
    for (const page of ['abc', '2.5', 2.5, '0', -3, '', null, true]) {
      expect(validateListSearch({ page })).toEqual({});
    }
  });

  it('caps the page at the highest the server accepts', () => {
    expect(validateListSearch({ page: '99999999' })).toEqual({ page: 1_000_000 });
  });

  it('drops an empty or non-string sort', () => {
    expect(validateListSearch({ sort: '' })).toEqual({});
    expect(validateListSearch({ sort: 3 })).toEqual({});
  });

  it('ignores other keys', () => {
    expect(validateListSearch({ q: 'web', page: '2' })).toEqual({ page: 2 });
  });
});
