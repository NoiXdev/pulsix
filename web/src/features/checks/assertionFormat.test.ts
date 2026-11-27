import { describe, expect, it } from 'vitest';
import { assertionOps, dialectSubjects, isAssertionPlugin, isNumericSubject, parseAssertion } from './assertionFormat.ts';

describe('the assertion grammar twin', () => {
  it('parses an entry into its parts', () => {
    expect(parseAssertion('ok=status in 200..299 @warn')).toEqual({
      name: 'ok',
      subject: 'status',
      arg: '',
      op: 'in',
      operand: '200..299',
      severity: 'warn',
    });
    expect(parseAssertion('rows=html("tr.item") count >= 2')).toEqual({
      name: 'rows',
      subject: 'html count',
      arg: 'tr.item',
      op: '>=',
      operand: '2',
      severity: 'crit',
    });
    expect(parseAssertion('ct=header("Content-Type") contains "json" @crit')).toEqual({
      name: 'ct',
      subject: 'header',
      arg: 'Content-Type',
      op: 'contains',
      operand: '"json"',
      severity: 'crit',
    });
    expect(parseAssertion('e=json("$.error") !exists')).toMatchObject({ subject: 'json', op: '!exists', operand: '' });
  });

  it('filters the operators by subject', () => {
    expect(assertionOps('status')).toEqual(['<', '<=', '>', '>=', '==', '!=', 'in']);
    expect(assertionOps('body')).not.toContain('exists');
    expect(assertionOps('header')).toContain('!exists');
    expect(isNumericSubject('html count')).toBe(true);
    expect(isNumericSubject('html')).toBe(false);
  });

  it('leaves RE2, selector and JSONPath syntax to the server', () => {
    // Refused by the server (TestAssertionFormat), so these cannot be
    // shared vectors: the form lets them through and the API names why.
    for (const entry of ['a=body matches "("', 'a=html("div[") exists', 'a=json("$[") exists', 'a=json("$[0,0]") exists', 'a=html("p:has(a)") exists', 'a=html("a ~ b") exists', 'a=html("x/**/p") exists', 'a=body matches ".{1000}"']) {
      expect(parseAssertion(entry)).not.toBeNull();
    }
  });
});

describe('the dialects of tcp, dns and tls (backlog 3.87)', () => {
  it('parses each plugin\'s own subjects and refuses the others\'', () => {
    expect(parseAssertion('b=response matches "^SSH" @warn', 'tcp')).toMatchObject({ subject: 'response', op: 'matches', severity: 'warn' });
    expect(parseAssertion('ip=answer("192.0.2.1") exists', 'dns')).toMatchObject({ subject: 'answer', arg: '192.0.2.1', op: 'exists', operand: '' });
    expect(parseAssertion('p=version >= 1.2', 'tls')).toMatchObject({ subject: 'version', op: '>=', operand: '1.2' });
    expect(parseAssertion('ok=status == 200', 'tcp')).toBeNull();
    expect(parseAssertion('c=connect_ms < 5')).toBeNull();
    expect(parseAssertion('a=answer("x") == "x"', 'dns')).toBeNull();
    expect(parseAssertion('a=answer("  ") exists', 'dns')).toBeNull();
  });

  it('knows which plugins have assertions and their subjects', () => {
    expect(isAssertionPlugin('tls')).toBe(true);
    expect(isAssertionPlugin('icmp')).toBe(false);
    expect(dialectSubjects('dns')).toEqual(['resolve_ms', 'answer_count', 'answers', 'answer']);
    expect(dialectSubjects('http')).toContain('html count');
    expect(assertionOps('answer')).toEqual(['exists', '!exists']);
    expect(assertionOps('cipher')).not.toContain('exists');
    expect(isNumericSubject('version')).toBe(true);
  });
});
