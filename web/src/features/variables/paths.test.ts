import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client.ts';
import { childPath, groupFields, hostFields, indexPath, inputId, membershipFields, problemAt, problemsFromError } from './paths.ts';
import type { VarSchema } from './types.ts';

const schema: VarSchema = {
  fields: [
    { name: 'env', type: 'string', required: true },
    { name: 'php_default', type: 'enum', options: ['8.3', '8.4'], default: '8.3', overridable: true, required: true },
    { name: 'vhosts', type: 'repeater', scope: 'host', fields: [{ name: 'domain', type: 'string' }] },
  ],
};

describe('paths', () => {
  it('builds paths in the server format', () => {
    expect(childPath('', 'vhosts')).toBe('vhosts');
    expect(childPath(indexPath('vhosts', 2), 'domain')).toBe('vhosts[2].domain');
    expect(problemAt([{ path: 'a', message: 'x' }], 'a')).toBe('x');
    expect(problemAt(undefined, 'a')).toBeUndefined();
  });

  it('builds a DOM-safe id from a prefix and a path', () => {
    expect(inputId('sf', 'vhosts[2].domain')).toBe('sf-vhosts-2--domain');
  });

  it('splits fields by scope like the server', () => {
    expect(groupFields(schema).map((f) => f.name)).toEqual(['env', 'php_default']);
    expect(hostFields(schema).map((f) => f.name)).toEqual(['vhosts']);
    const m = membershipFields(schema);
    expect(m.map((f) => f.name)).toEqual(['php_default', 'vhosts']);
    expect(m[0].required).toBe(false);
    expect(m[0].default).toBeUndefined();
    expect(schema.fields[1].default).toBe('8.3');
  });

  it('maps 422 details under a location to problems', () => {
    const err = new ApiError(422, {
      errors: [
        { location: 'body.vars.vhosts[0].domain', message: 'is required' },
        { location: 'body.vars', message: 'root problem' },
        { location: 'body.schema.fields[0].name', message: 'elsewhere' },
        { message: 'no location' },
      ],
    });
    expect(problemsFromError(err, 'body.vars')).toEqual([
      { path: 'vhosts[0].domain', message: 'is required' },
      { path: '', message: 'root problem' },
    ]);
    expect(problemsFromError(new ApiError(400, {}), 'body.vars')).toEqual([]);
    expect(problemsFromError(new Error('x'), 'body.vars')).toEqual([]);
  });
});
