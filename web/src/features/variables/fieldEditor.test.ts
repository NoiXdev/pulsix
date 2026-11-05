import { describe, expect, it } from 'vitest';
import { changeType, fieldPath, formatAttribute, formatValue, hasProblemUnder, moveField, newField, removeField, replaceField, typeLabel, typesFor } from './fieldEditor.ts';
import { en } from '../../i18n/en/index.ts';
import type { SchemaField } from './types.ts';

describe('field editor helpers', () => {
  it('labels types like the mockup', () => {
    expect(typeLabel({ name: 'a', type: 'list', item: { name: '', type: 'int' } }, en)).toBe('list<int>');
    expect(typeLabel({ name: 'a', type: 'object', fields: [] }, en)).toBe('sub-schema');
    expect(typeLabel({ name: 'a', type: 'repeater' }, en)).toBe('repeater');
  });

  it('names new fields uniquely', () => {
    expect(newField([])).toEqual({ name: 'field_1', type: 'string' });
    expect(newField([{ name: 'field_2', type: 'string' }]).name).toBe('field_3');
    expect(newField([{ name: 'a', type: 'string' }, { name: 'field_2', type: 'int' }]).name).toBe('field_3');
  });

  it('changes a type and keeps only attributes that still apply', () => {
    const enumField: SchemaField = { name: 'php', label: 'PHP', type: 'enum', options: ['8.3'], default: '8.3', scope: 'host', required: true };
    expect(changeType(enumField, 'string')).toEqual({ name: 'php', label: 'PHP', type: 'string', scope: 'host', required: true });
    expect(changeType({ name: 'x', type: 'string' }, 'enum').options).toEqual(['option_1']);
    expect(changeType({ name: 'x', type: 'string' }, 'list').item).toEqual({ name: '', type: 'string' });
    expect(changeType({ name: 'x', type: 'string' }, 'repeater').fields).toEqual([{ name: 'value', type: 'string' }]);
    const list: SchemaField = { name: 'l', type: 'list', item: { name: '', type: 'string' }, min_items: 1, max_items: 3 };
    expect(changeType(list, 'repeater')).toMatchObject({ min_items: 1, max_items: 3 });
    expect(changeType({ name: 'n', type: 'int', min: 1, overridable: true }, 'float')).toEqual({ name: 'n', type: 'float', overridable: true });
    const obj: SchemaField = { name: 'o', type: 'object', fields: [{ name: 'k', type: 'int' }] };
    expect(changeType(obj, 'repeater').fields).toEqual(obj.fields);
  });

  it('moves, replaces and removes without mutating', () => {
    const fields: SchemaField[] = [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }, { name: 'c', type: 'string' }];
    expect(moveField(fields, 0, 2).map((f) => f.name)).toEqual(['b', 'c', 'a']);
    expect(replaceField(fields, 1, { name: 'z', type: 'int' })[1].name).toBe('z');
    expect(removeField(fields, 0).map((f) => f.name)).toEqual(['b', 'c']);
    expect(fields.map((f) => f.name)).toEqual(['a', 'b', 'c']);
  });

  it('builds schema paths and finds problems below them', () => {
    expect(fieldPath('', 2)).toBe('fields[2]');
    expect(fieldPath('fields[2]', 0)).toBe('fields[2].fields[0]');
    const problems = [{ path: 'fields[1].fields[0].name', message: 'x' }];
    expect(hasProblemUnder(problems, 'fields[1]')).toBe(true);
    expect(hasProblemUnder(problems, 'fields[10]')).toBe(false);
    expect(hasProblemUnder([{ path: 'fields[1]', message: 'x' }], 'fields[1]')).toBe(true);
    expect(hasProblemUnder(undefined, 'fields[0]')).toBe(false);
  });

  it('keeps a field\'s provenance when its type changes', () => {
    const src = { kind: 'marker' as const, file: 'site.yml', line: 2 };
    const f: SchemaField = { name: 'n', type: 'int', min: 1, active: false, origin: 'source', source: src, set_in_playbook: true };
    expect(changeType(f, 'string')).toEqual({ name: 'n', type: 'string', active: false, origin: 'source', source: src, set_in_playbook: true });
  });

  it('withholds the secret type from shared definitions only', () => {
    expect(typesFor('shared')).not.toContain('secret');
    expect(typesFor('group')).toContain('secret');
    expect(typesFor('template')).toContain('secret');
  });

  it('formats attribute values in one line', () => {
    expect(formatValue(undefined)).toBe('—');
    expect(formatValue(80)).toBe('80');
    expect(formatValue(['a', 'b'])).toBe('a, b');
    expect(formatValue(true)).toBe('true');
    expect(formatAttribute('fields', [{ name: 'domain', type: 'string' }, { name: 'ssl', type: 'bool' }])).toBe('domain, ssl');
    expect(formatAttribute('item', { name: '', type: 'int' })).toBe('int');
    expect(formatAttribute('item', { name: '', type: 'enum', options: ['a', 'b'] })).toBe('enum (a, b)');
  });
});
