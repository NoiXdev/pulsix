import { describe, expect, it } from 'vitest';
import { echoedVars, toDraftVars, toEditableVars, varsForPut } from './vars.ts';
import type { SchemaField } from '../variables/types.ts';

const schema: SchemaField[] = [
  { name: 'token', type: 'secret', required: true },
  { name: 'region', type: 'string' },
  {
    name: 'targets',
    type: 'repeater',
    fields: [
      { name: 'host', type: 'string' },
      { name: 'password', type: 'secret' },
    ],
  },
];

const SET = { $secret: 'set' };

describe('varsForPut (masked-secret roundtrip)', () => {
  // The server seals secret extra vars and resolves keep markers: an
  // unchanged secret must travel as a keep marker — omitting it would drop
  // the stored secret on the PUT (full replace).
  it('sends an unchanged secret field as a keep marker', () => {
    const vars = varsForPut(schema, { token: { $secret: 'keep', from: 'token' }, region: 'eu' });
    expect(vars).toEqual({ token: { $secret: 'keep', from: 'token' }, region: 'eu' });
  });

  it('turns a raw set-marker under a declared secret into a keep marker', () => {
    const vars = varsForPut(schema, { token: SET, region: 'eu' });
    expect(vars).toEqual({ token: { $secret: 'keep', from: 'token' }, region: 'eu' });
  });

  it('sends a replaced secret as new plaintext', () => {
    const vars = varsForPut(schema, { token: 'hunter2', region: 'eu' });
    expect(vars).toEqual({ token: 'hunter2', region: 'eu' });
  });

  it('keeps an unchanged secret inside a repeater entry as a keep marker', () => {
    const vars = varsForPut(schema, {
      token: SET,
      targets: [
        { host: 'a', password: SET },
        { host: 'b', password: 'plain' },
      ],
    });
    expect(vars).toEqual({
      token: { $secret: 'keep', from: 'token' },
      targets: [{ host: 'a', password: { $secret: 'keep', from: 'targets[0].password' } }, { host: 'b', password: 'plain' }],
    });
  });

  it('strips a stored secret the schema no longer declares', () => {
    const vars = varsForPut(undefined, { token: SET, region: 'eu' });
    expect(vars).toEqual({ region: 'eu' });
  });

  it('declaredOnly: keys the schema does not declare are dropped', () => {
    const vars = varsForPut(schema, { token: 'x', orphan: 1 });
    expect(vars).toEqual({ token: 'x' });
  });

  // Plan decision 10: the server keeps the stored value of an inactive
  // field the body omits; the SPA sends the active fields' values only.
  it('sends the values of active fields only', () => {
    const fields: SchemaField[] = [{ ...schema[0], active: false }, schema[1]];
    expect(varsForPut(fields, { token: SET, region: 'eu' })).toEqual({ region: 'eu' });
  });

  // A secret removed from the schema and added back under another type:
  // the server resolves keep markers for secret fields only.
  it('sends no keep marker under a field that is not secret', () => {
    expect(varsForPut([{ name: 'old', type: 'string' }], { old: { $secret: 'keep', from: 'old' } })).toEqual({});
    expect(varsForPut([{ name: 'old', type: 'string' }], { old: SET })).toEqual({});
  });

  it('sends nothing under a schema whose fields are all inactive', () => {
    expect(varsForPut([{ name: 'region', type: 'string', active: false }], { region: 'eu' })).toEqual({});
    expect(varsForPut([], { region: 'eu' })).toEqual({});
  });
});

describe('toEditableVars', () => {
  it('turns set-markers into keep markers and drops undeclared keys', () => {
    const editable = toEditableVars(schema, { token: SET, orphan: 1 });
    expect(editable).toEqual({ token: { $secret: 'keep', from: 'token' } });
  });
});

describe('echoedVars', () => {
  it('sends the loaded active values back, each stored secret as a keep marker', () => {
    const fields: SchemaField[] = [...schema.slice(0, 2), { name: 'debug', type: 'bool', active: false }];
    expect(echoedVars({ fields }, { token: SET, region: 'eu', debug: true, orphan: 1 })).toEqual({
      token: { $secret: 'keep', from: 'token' },
      region: 'eu',
    });
  });

  it('sends every value of a template without a schema', () => {
    expect(echoedVars(undefined, { region: 'eu' })).toEqual({ region: 'eu' });
  });
});

describe('toDraftVars', () => {
  it('keeps the values of removed fields, each stored secret as a keep marker', () => {
    expect(toDraftVars(schema, { token: SET, region: 'eu', old: 'kept', old_secret: SET })).toEqual({
      token: { $secret: 'keep', from: 'token' },
      region: 'eu',
      old: 'kept',
      old_secret: { $secret: 'keep', from: 'old_secret' },
    });
  });
});
