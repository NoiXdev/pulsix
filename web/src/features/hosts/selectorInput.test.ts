import { describe, expect, it } from 'vitest';
import { parseTagChip } from './selectorInput.ts';

describe('parseTagChip', () => {
  it('turns "role=db" into {key: "role", value: "db"}', () => {
    expect(parseTagChip('role=db')).toEqual({ ok: true, tag: { key: 'role', value: 'db' } });
  });

  it('rejects "Role=db" with an inline error (uppercase key)', () => {
    const result = parseTagChip('Role=db');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toMatch(/not a valid tag key/);
    }
  });

  it('trims surrounding whitespace from both key and value', () => {
    expect(parseTagChip('  env = prod  ')).toEqual({ ok: true, tag: { key: 'env', value: 'prod' } });
  });

  it('allows an empty value ("exists"-style tag)', () => {
    expect(parseTagChip('managed=')).toEqual({ ok: true, tag: { key: 'managed', value: '' } });
  });

  it('accepts dots, underscores and hyphens in the key', () => {
    expect(parseTagChip('kernel.version=6.12')).toEqual({
      ok: true,
      tag: { key: 'kernel.version', value: '6.12' },
    });
  });

  it('rejects input with no "=" at all', () => {
    const result = parseTagChip('justsometext');
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toMatch(/key=value/);
    }
  });

  it('rejects empty input', () => {
    const result = parseTagChip('   ');
    expect(result.ok).toBe(false);
  });

  it('rejects a key that starts with a hyphen or underscore', () => {
    const result = parseTagChip('-role=db');
    expect(result.ok).toBe(false);
  });
});
