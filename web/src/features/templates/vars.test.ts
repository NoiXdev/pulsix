import { describe, expect, it } from 'vitest';
import { toEditableVars, varsForPut } from './vars.ts';
import type { SchemaField } from '../inventory/schema/types.ts';

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
});

describe('toEditableVars', () => {
  it('turns set-markers into keep markers and drops undeclared keys', () => {
    const editable = toEditableVars(schema, { token: SET, orphan: 1 });
    expect(editable).toEqual({ token: { $secret: 'keep', from: 'token' } });
  });
});
