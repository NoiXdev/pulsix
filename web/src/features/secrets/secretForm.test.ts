import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { fieldsOf, MAX_FIELDS, validateSecret, type FieldRow } from './secretForm.ts';

const ok: FieldRow[] = [{ name: 'community', value: 'public' }];
const f = en.secrets.errors;

describe('validateSecret', () => {
  it('accepts a valid name and fields', () => {
    expect(validateSecret(en, 'switch-core', ok, [])).toBeNull();
  });

  it.each([
    ['', f.name],
    ['bad name', f.name],
    ['x'.repeat(65), f.name],
    ['a/b', f.name],
  ])('refuses the secret name %j', (name, message) => {
    expect(validateSecret(en, name, ok, [])?.name).toBe(message);
  });

  it('accepts the whole name alphabet and 64 characters', () => {
    expect(validateSecret(en, 'Az09_.-', ok, [])).toBeNull();
    expect(validateSecret(en, 'x'.repeat(64), ok, [])).toBeNull();
  });

  it('refuses a name that is taken when creating, not when replacing', () => {
    expect(validateSecret(en, 'sw', ok, ['sw'])?.name).toBe(f.nameTaken);
    expect(validateSecret(en, 'sw', ok, null)).toBeNull();
  });

  it('refuses bad, empty and repeated field names', () => {
    const rows: FieldRow[] = [
      { name: 'ok', value: 'v' },
      { name: '', value: 'v' },
      { name: 'bad name', value: 'v' },
      { name: 'ok', value: 'v' },
    ];
    expect(validateSecret(en, 'sw', rows, [])?.rows).toEqual([
      {},
      { name: f.fieldName },
      { name: f.fieldName },
      { name: f.fieldRepeated },
    ]);
  });

  it('refuses an empty, an oversized and a NUL value, counting bytes', () => {
    const rows: FieldRow[] = [
      { name: 'a', value: '' },
      { name: 'b', value: 'x'.repeat(4097) },
      { name: 'c', value: 'ä'.repeat(2049) },
      { name: 'd', value: 'a\u0000b' },
      { name: 'e', value: 'ä'.repeat(2048) },
    ];
    expect(validateSecret(en, 'sw', rows, [])?.rows).toEqual([
      { value: en.validation.required },
      { value: f.valueTooLong },
      { value: f.valueTooLong },
      { value: f.valueNul },
      {},
    ]);
  });

  it('needs one to sixteen fields', () => {
    expect(validateSecret(en, 'sw', [], [])?.form).toBe(f.fieldCount);
    const many = Array.from({ length: MAX_FIELDS + 1 }, (_, i) => ({ name: `f${i}`, value: 'v' }));
    expect(validateSecret(en, 'sw', many, [])?.form).toBe(f.fieldCount);
    expect(validateSecret(en, 'sw', many.slice(0, MAX_FIELDS), [])).toBeNull();
  });
});

describe('fieldsOf', () => {
  it('maps the rows to the request body, values untouched', () => {
    expect(fieldsOf([{ name: 'user', value: ' ops ' }, { name: 'auth_pass', value: 'p' }])).toEqual({ user: ' ops ', auth_pass: 'p' });
  });
});
