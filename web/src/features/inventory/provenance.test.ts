import { describe, expect, it } from 'vitest';
import { ancestorVarsFrom, formatValue, provenanceOf, type AncestorVars } from './provenance.ts';
import type { SchemaField } from '../variables/types.ts';

const env: SchemaField = { name: 'env', type: 'string' };
const level: SchemaField = { name: 'level', type: 'enum', options: ['info', 'debug'], default: 'info' };

const known = (name: string, vars: Record<string, unknown>): AncestorVars => ({ name, vars });
const unknown = (name: string): AncestorVars => ({ name, vars: undefined });

describe('provenanceOf', () => {
  it.each([
    ['an own value over the nearest ancestor setting it', env, 'prod', [known('all_servers', { env: 'staging' }), known('eu', {})], { kind: 'overrides', group: 'all_servers', value: 'staging' }],
    ['the nearest of two ancestors', env, 'prod', [known('root', { env: 'a' }), known('eu', { env: 'b' })], { kind: 'overrides', group: 'eu', value: 'b' }],
    ['an own value nobody above sets', env, 'prod', [known('root', {})], null],
    ['an unset field inheriting', env, undefined, [known('all_servers', { env: 'ntp.example.test' })], { kind: 'inherits', group: 'all_servers', value: 'ntp.example.test' }],
    ['an unset field nobody sets', env, undefined, [known('root', { other: 1 })], null],
    ['an unset field with a default, whatever is above', level, undefined, [known('root', { level: 'debug' })], { kind: 'default', value: 'info' }],
    ['a set field with a default', level, 'debug', [known('root', { level: 'info' })], { kind: 'overrides', group: 'root', value: 'info' }],
    ['a nearer unknown ancestor', env, 'prod', [known('root', { env: 'a' }), unknown('eu')], null],
    ['a nearer unknown ancestor of an unset field', env, undefined, [known('root', { env: 'a' }), unknown('eu')], null],
    ['an unknown ancestor past the answer', env, undefined, [unknown('root'), known('eu', { env: 'b' })], { kind: 'inherits', group: 'eu', value: 'b' }],
    ['no ancestors', env, 'prod', [], null],
    ['a top-level group with a default', level, undefined, [], { kind: 'default', value: 'info' }],
    // schema.Resolve writes no nil default: a null one is none.
    ['a null default, which renders nothing', { ...env, default: null }, undefined, [known('root', { env: 'a' })], { kind: 'inherits', group: 'root', value: 'a' }],
  ])('%s', (_, field, own, ancestors, want) => {
    expect(provenanceOf(field, own, ancestors)).toEqual(want);
  });
});

describe('ancestorVarsFrom', () => {
  it('reads each ancestor from the preview, unknown when it is not there', () => {
    const inventory = { all: { children: ['prod'] }, prod: { vars: { env: 'prod' }, children: ['web'] }, eu: { children: ['web'] } };
    expect(ancestorVarsFrom(inventory, [{ name: 'prod' }, { name: 'eu' }, { name: 'gone' }, { name: 'toString' }])).toEqual([
      { name: 'prod', vars: { env: 'prod' } },
      { name: 'eu', vars: {} },
      { name: 'gone', vars: undefined },
      { name: 'toString', vars: undefined },
    ]);
    expect(ancestorVarsFrom(undefined, [{ name: 'prod' }])).toEqual([{ name: 'prod', vars: undefined }]);
  });
});

describe('formatValue', () => {
  it('quotes strings as they are and the rest as JSON, cut short', () => {
    expect(formatValue('staging')).toBe('staging');
    expect(formatValue(8080)).toBe('8080');
    expect(formatValue(false)).toBe('false');
    expect(formatValue(['a', 'b'])).toBe('["a","b"]');
    expect(formatValue('x'.repeat(60))).toBe(`${'x'.repeat(47)}…`);
  });
});
