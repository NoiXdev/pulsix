import { describe, expect, it } from 'vitest';
import { assertionOps, isNumericSubject, parseAssertion } from './assertionFormat.ts';

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
