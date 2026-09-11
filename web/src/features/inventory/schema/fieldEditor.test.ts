import { describe, expect, it } from 'vitest';
import { changeType, fieldPath, hasProblemUnder, moveField, newField, removeField, replaceField, typeLabel } from './fieldEditor.ts';
import type { SchemaField } from './types.ts';

describe('field editor helpers', () => {
  it('labels types like the mockup', () => {
    expect(typeLabel({ name: 'a', type: 'list', item: { name: '', type: 'int' } })).toBe('list<int>');
    expect(typeLabel({ name: 'a', type: 'object', fields: [] })).toBe('sub-schema');
    expect(typeLabel({ name: 'a', type: 'repeater' })).toBe('repeater');
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
});
