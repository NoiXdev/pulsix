import { describe, expect, it } from 'vitest';
import { activeFields, editLocked, isActive, keepAsManual, originOf, resetToSource, sourceDifferences } from './origin.ts';
import type { FieldSource, SchemaField } from './types.ts';

const src: FieldSource = { kind: 'argument_spec', role: 'web', entry_point: 'main', file: 'roles/web/meta/argument_specs.yml', line: 4 };
const port: SchemaField = { name: 'port', type: 'int', default: 80, origin: 'source', source: src };

describe('isActive and originOf', () => {
  it('reads an absent active as active and an absent origin as manual', () => {
    expect(isActive({} as SchemaField)).toBe(true);
    expect(isActive({ name: 'a', type: 'string', active: false })).toBe(false);
    expect(originOf({ name: 'a', type: 'string' })).toBe('manual');
    expect(originOf(port)).toBe('source');
  });

  it('keeps only the active fields', () => {
    const fields: SchemaField[] = [{ name: 'a', type: 'string' }, { name: 'b', type: 'string', active: false }, { name: 'c', type: 'string', active: true }];
    expect(activeFields(fields).map((f) => f.name)).toEqual(['a', 'c']);
  });
});

describe('editLocked', () => {
  it('makes a source field adjusted on a content change and keeps the source version', () => {
    const next = editLocked(port, { ...port, label: 'HTTP port' });
    expect(next).toEqual({ name: 'port', type: 'int', default: 80, label: 'HTTP port', origin: 'adjusted', source: src, source_field: { name: 'port', type: 'int', default: 80 } });
  });

  it('keeps a source field locked when only active changes', () => {
    expect(editLocked(port, { ...port, active: false })).toEqual({ ...port, active: false });
  });

  it('makes a sub-field edit adjust the whole top-level field', () => {
    const vhosts: SchemaField = { name: 'vhosts', type: 'repeater', origin: 'source', source: src, fields: [{ name: 'domain', type: 'string' }] };
    const next = editLocked(vhosts, { ...vhosts, fields: [{ name: 'domain', type: 'string', required: true }] });
    expect(originOf(next)).toBe('adjusted');
    expect(next.source_field?.fields).toEqual([{ name: 'domain', type: 'string' }]);
  });

  it('leaves an adjusted field adjusted with its own source version', () => {
    const adjusted: SchemaField = { ...port, default: 8080, origin: 'adjusted', source_field: { name: 'port', type: 'int', default: 80 } };
    const next = editLocked(adjusted, { ...adjusted, label: 'Port' });
    expect(next.origin).toBe('adjusted');
    expect(next.source_field).toEqual({ name: 'port', type: 'int', default: 80 });
  });

  it('leaves manual and gone fields as they are', () => {
    const manual: SchemaField = { name: 'm', type: 'string' };
    expect(editLocked(manual, { ...manual, label: 'M' })).toEqual({ name: 'm', type: 'string', label: 'M' });
    const gone: SchemaField = { ...port, origin: 'gone' };
    expect(editLocked(gone, { ...gone, label: 'P' }).origin).toBe('gone');
  });

  it('does not count attribute order or an absent attribute as a change', () => {
    const reordered: SchemaField = { default: 80, source: src, origin: 'source', type: 'int', name: 'port', label: undefined };
    expect(originOf(editLocked(port, reordered))).toBe('source');
  });
});

describe('resetToSource', () => {
  it('restores the source content and keeps the field inactive', () => {
    const adjusted: SchemaField = { name: 'port', type: 'int', default: 8080, label: 'Port', active: false, origin: 'adjusted', source: src, set_in_playbook: true, source_field: { name: 'port', type: 'int', default: 80 } };
    expect(resetToSource(adjusted)).toEqual({ name: 'port', type: 'int', default: 80, active: false, origin: 'source', source: src, set_in_playbook: true });
  });
});

describe('keepAsManual', () => {
  it('turns a gone field manual and drops every provenance but active', () => {
    const gone: SchemaField = { ...port, active: false, origin: 'gone', set_in_playbook: true, source_field: { name: 'port', type: 'int' } };
    const next = keepAsManual(gone);
    expect(originOf(next)).toBe('manual');
    expect(next).toEqual({ name: 'port', type: 'int', default: 80, active: false });
  });
});

describe('sourceDifferences', () => {
  it('lists each attribute that differs from the source with what the source says', () => {
    const adjusted: SchemaField = { ...port, default: 8080, label: 'Port', origin: 'adjusted', source_field: { name: 'port', type: 'int', default: 80, required: true } };
    expect(sourceDifferences(adjusted)).toEqual([['default', 80], ['label', undefined], ['required', true]]);
    expect(sourceDifferences(port)).toEqual([]);
  });
});
