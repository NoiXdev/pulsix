/// <reference types="node" />
// The vectors are read from disk at run time rather than imported: the
// file lives in the Go package (internal/checkplugins/testdata), outside
// web/, and the image's SPA build stage has only web/ — a static import
// would fail `tsc -b` there. CI runs Vitest from a full checkout.
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import fixture from '../../test/checkPlugins.json';
import { FORMATS, formatProblem, formatValid, httpHeaderProblem } from './paramFormats.ts';

interface Vector {
  format: string;
  input: string;
  valid: boolean;
}

const VECTOR_FILE = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../internal/checkplugins/testdata/formats.json');
const VECTORS: Vector[] = JSON.parse(readFileSync(VECTOR_FILE, 'utf8'));

describe('the parameter formats', () => {
  it('read the shared vector file', () => {
    expect(VECTORS.length).toBeGreaterThan(100);
  });

  it.each(VECTORS.map((v, i) => [i, v.format, JSON.stringify(v.input), v] as const))(
    '#%i %s %s',
    (_i, _format, _input, v) => {
      expect(formatValid(v.format, v.input)).toBe(v.valid);
    },
  );

  it('have a twin for every format the vectors name', () => {
    // The Go side pins that the vectors name every format a validator
    // knows (TestFormatVectorsCoverEveryFormat), so this covers them all.
    const named = new Set(VECTORS.map((v) => v.format));
    expect([...named].filter((f) => !(f in FORMATS))).toEqual([]);
    expect(Object.keys(FORMATS).sort()).toEqual([...named].sort());
  });

  it('have a twin for every format a descriptor uses', () => {
    const used = fixture.plugins.flatMap((p) => p.params.flatMap((s) => ('format' in s && s.format ? [s.format] : [])));
    expect(used.filter((f) => !(f in FORMATS))).toEqual([]);
  });

  it('leave a JSON value’s path syntax to the server, which parses it (backlog 3.6h)', () => {
    // Refused by the server's JSONPath parser (TestJSONValueFormat), so
    // these cannot be shared vectors: the form lets them through and the
    // server's 400 names the parser's reason.
    for (const entry of ['a=$[', 'a=$.', 'a=$x', "a=$['open", 'a=$[0,0]', 'a=$[?count(@) > 1]', 'a=$[?@.a == $.b]']) {
      expect(formatValid('jsonValue', entry)).toBe(true);
    }
    expect(formatProblem('jsonValue', 'a=.a')).toBe('jsonValue');
    expect(formatProblem('jsonValue', 'status=$.status')).toBeNull();
  });

  it('name why a request header is refused, so the message can point to the secret (backlog 3.84)', () => {
    expect(httpHeaderProblem('X-Api-Version: 2')).toBeNull();
    expect(formatProblem('httpHeader', 'Authorization: Bearer x')).toBe('httpHeaderCredentials');
    expect(formatProblem('httpHeader', 'cookie: a=b')).toBe('httpHeaderCredentials');
    expect(formatProblem('httpHeader', 'Host: example.com')).toBe('httpHeaderReserved');
    expect(formatProblem('httpHeader', 'content-type: text/plain')).toBe('httpHeaderContentType');
    expect(formatProblem('httpHeader', 'X-A: a\nb')).toBe('httpHeader');
    expect(formatProblem('httpHeader', 'Authorization')).toBe('httpHeader');
    expect(formatProblem('httpHeader', 'X-Api-Key: k')).toBe('httpHeaderCredentials');
    expect(formatProblem('httpHeader', 'X-Forwarded-For: 1.2.3.4')).toBe('httpHeaderRouting');
  });

  it('accept nothing for a format no twin knows, as the server does', () => {
    expect(formatValid('nope', '')).toBe(false);
    expect(formatValid('nope', 'x')).toBe(false);
    expect(formatValid('constructor', 'x')).toBe(false);
  });

  it('name the control character, not the path shape, for a device path carrying one', () => {
    expect(formatProblem('devicePath', '/dev/sd\ta')).toBe('noControl');
    expect(formatProblem('devicePath', 'sda')).toBe('devicePath');
    expect(formatProblem('devicePath', '/dev/sda')).toBeNull();
    expect(formatProblem('url', 'ftp://x')).toBe('url');
  });
});
