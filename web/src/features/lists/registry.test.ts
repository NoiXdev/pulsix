import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import { LIST_COLUMNS, listName, type ListId } from './registry.ts';

// The server refuses the whole preferences document when one id breaks this
// (internal/preferences): a bad column id would fail every later save.
const PREFERENCE_ID = /^[a-z][a-z0-9_.-]{0,63}$/;

describe('the list registry', () => {
  it('uses only ids the preferences API accepts', () => {
    for (const [list, columns] of Object.entries(LIST_COLUMNS)) {
      expect(list).toMatch(PREFERENCE_ID);
      for (const column of columns) expect(column, `a column of ${list}`).toMatch(PREFERENCE_ID);
    }
  });

  it('names no column twice in one list', () => {
    for (const columns of Object.values(LIST_COLUMNS)) expect(new Set(columns).size).toBe(columns.length);
  });

  it('has a name for every list in both catalogues', () => {
    const ids = Object.keys(LIST_COLUMNS).sort();
    expect(Object.keys(en.lists.names).sort()).toEqual(ids);
    expect(Object.keys(de.lists.names).sort()).toEqual(ids);
  });
});

describe('listName', () => {
  it('names a known list in the given language', () => {
    expect(listName('hosts' satisfies ListId, en)).toBe('Hosts');
    expect(listName('runs', de)).toBe('Läufe');
  });

  it('answers an unknown id with the id', () => {
    expect(listName('gone', en)).toBe('gone');
    expect(listName('constructor', en)).toBe('constructor');
  });
});
