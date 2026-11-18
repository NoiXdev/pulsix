import { describe, expect, it } from 'vitest';
import {
  formatList,
  hasDefault,
  parseList,
  toggledDefault,
  toggledScope,
  withActive,
  withDefault,
  withOption,
  withRequired,
  withText,
  withoutOption,
} from './fieldEdits.ts';
import type { SchemaField } from '../../variables/types.ts';

const env: SchemaField = { name: 'env', type: 'string' };
const tier: SchemaField = { name: 'tier', type: 'enum', options: ['gold', 'silver'], default: 'gold' };

describe('field edits', () => {
  it('toggles the scope, dropping "hosts may override" for a host field', () => {
    const host = toggledScope({ ...env, overridable: true });
    expect(host).toEqual({ name: 'env', type: 'string', scope: 'host' });
    expect(toggledScope(host)).toEqual(env);
    expect(Object.hasOwn(toggledScope(host), 'scope')).toBe(false);
  });

  it('sets and clears required and active without leaving keys behind', () => {
    expect(withRequired(env, true)).toEqual({ ...env, required: true });
    expect(Object.keys(withRequired({ ...env, required: true }, false))).toEqual(['name', 'type']);
    expect(withActive(env, false)).toEqual({ ...env, active: false });
    expect(Object.keys(withActive({ ...env, active: false }, true))).toEqual(['name', 'type']);
  });

  it('sets text attributes, an empty one removed', () => {
    expect(withText(env, 'label', 'Environment')).toEqual({ ...env, label: 'Environment' });
    expect(Object.keys(withText({ ...env, label: 'x' }, 'label', ''))).toEqual(['name', 'type']);
    expect(Object.keys(withDefault({ ...env, default: 'a' }, undefined))).toEqual(['name', 'type']);
  });

  it('adds an option once and ignores a blank one', () => {
    expect(withOption(tier, ' bronze ').options).toEqual(['gold', 'silver', 'bronze']);
    expect(withOption(tier, 'gold')).toBe(tier);
    expect(withOption(tier, '  ')).toBe(tier);
    expect(withOption({ name: 'x', type: 'enum' }, 'a').options).toEqual(['a']);
  });

  it('removes an option and the default with it', () => {
    expect(withoutOption(tier, 'silver')).toEqual({ ...tier, options: ['gold'] });
    expect(withoutOption(tier, 'gold')).toEqual({ name: 'tier', type: 'enum', options: ['silver'] });
  });

  it('makes an option the default, and a second click clears it', () => {
    expect(toggledDefault(tier, 'silver').default).toBe('silver');
    expect(Object.hasOwn(toggledDefault(tier, 'gold'), 'default')).toBe(false);
  });

  it('knows which types take a default', () => {
    expect(['string', 'int', 'float', 'bool', 'enum', 'list'].every((t) => hasDefault(t as SchemaField['type']))).toBe(true);
    expect(['secret', 'repeater', 'object'].some((t) => hasDefault(t as SchemaField['type']))).toBe(false);
  });

  it.each([
    ['a, b ,, c', 'string', ['a', 'b', 'c']],
    ['1, 2.5, x', 'float', [1, 2.5, 'x']],
    ['true, false, maybe', 'bool', [true, false, 'maybe']],
    ['  ', 'string', undefined],
  ] as const)('parses the list %j as %s', (text, item, want) => {
    expect(parseList(text, item)).toEqual(want);
  });

  it('formats a list default as comma separated text', () => {
    expect(formatList(['a', 1, true])).toBe('a, 1, true');
    expect(formatList(undefined)).toBe('');
  });
});
