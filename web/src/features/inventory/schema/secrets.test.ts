import { describe, expect, it } from 'vitest';
import { declaredOnly, isKeepMarker, isSecretSet, keepMarker, toKeepMarkers } from './secrets.ts';
import type { SchemaField } from './types.ts';

const fields: SchemaField[] = [
  { name: 'db_password', type: 'secret' },
  { name: 'auth', type: 'object', fields: [{ name: 'password', type: 'secret' }] },
  { name: 'vhosts', type: 'repeater', fields: [{ name: 'password', type: 'secret' }, { name: 'domain', type: 'string' }] },
];
const SET = { $secret: 'set' };

describe('secret helpers', () => {
  it('recognises masks and keep markers', () => {
    expect(isSecretSet(SET)).toBe(true);
    expect(isSecretSet('x')).toBe(false);
    expect(isKeepMarker(keepMarker('a[0].b'))).toBe(true);
    expect(isKeepMarker({ $secret: 'keep' })).toBe(false);
    expect(isKeepMarker(null)).toBe(false);
  });

  it('turns every mask into a keep marker that remembers its path', () => {
    const vars = {
      db_password: SET,
      auth: { password: SET },
      vhosts: [{ domain: 'a.io', password: SET }, { domain: 'b.io' }, 'odd'],
      untouched: SET,
    };
    expect(toKeepMarkers(fields, vars)).toEqual({
      db_password: keepMarker('db_password'),
      auth: { password: keepMarker('auth.password') },
      vhosts: [{ domain: 'a.io', password: keepMarker('vhosts[0].password') }, { domain: 'b.io' }, 'odd'],
      untouched: SET,
    });
    expect(vars.db_password).toBe(SET);
  });

  it('drops keys the schema no longer declares', () => {
    expect(declaredOnly(fields, { db_password: 'x', removed: 1 })).toEqual({ db_password: 'x' });
  });

  it('drops orphaned keys at every depth, like the server validates', () => {
    const vars = {
      db_password: 'x',
      auth: { password: 'y', removed_sub_field: 'gone' },
      vhosts: [
        { password: 'z', domain: 'a.io', removed_sub_field: 'gone' },
        { password: keepMarker('vhosts[1].password'), domain: 'b.io' },
      ],
    };
    expect(declaredOnly(fields, vars)).toEqual({
      db_password: 'x',
      auth: { password: 'y' },
      vhosts: [
        { password: 'z', domain: 'a.io' },
        { password: keepMarker('vhosts[1].password'), domain: 'b.io' },
      ],
    });
  });

  it('does not mutate the input and leaves non-object repeater entries and list items alone', () => {
    const vars = {
      db_password: 'x',
      removed: 1,
      auth: { password: 'y', removed_sub_field: 'gone' },
      vhosts: [{ password: 'z', domain: 'a.io', removed_sub_field: 'gone' }, 'odd'],
    };
    const snapshot = JSON.parse(JSON.stringify(vars)) as typeof vars;
    const listFields: SchemaField[] = [{ name: 'aliases', type: 'list', item: { name: '', type: 'string' } }];
    expect(declaredOnly(listFields, { aliases: ['a', 'b'], removed: 1 })).toEqual({ aliases: ['a', 'b'] });
    declaredOnly(fields, vars);
    expect(vars).toEqual(snapshot);
  });
});
