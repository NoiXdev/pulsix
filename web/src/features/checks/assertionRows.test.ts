import { describe, expect, it } from 'vitest';
import {
  canonicalNumber, composeExpression, composeRow, composeRows, emptyRow, entryNamedBy, parseRows, rowFromEntry, withOp, withSubject,
  type AssertionRow,
} from './assertionRows.ts';

const row = (over: Partial<AssertionRow>): AssertionRow => ({ ...emptyRow(), ...over });

describe('the assertion editor rows', () => {
  it.each([
    ['ok=status in 200..299 @warn', row({ name: 'ok', subject: 'status', op: 'in', operand: '200', hi: '299', severity: 'warn' })],
    ['fast=response_ms < 800', row({ name: 'fast', subject: 'response_ms', op: '<', operand: '800' })],
    ['ct=header("Content-Type") contains "json"', row({ name: 'ct', subject: 'header', arg: 'Content-Type', op: 'contains', operand: 'json' })],
    ['e=json("$.error") !exists', row({ name: 'e', subject: 'json', arg: '$.error', op: '!exists' })],
    ['rows=html("tr.item > td") count >= 2', row({ name: 'rows', subject: 'html count', arg: 'tr.item > td', op: '>=', operand: '2' })],
    ['t=html("h1") == "Say \\"hi\\""', row({ name: 't', subject: 'html', arg: 'h1', op: '==', operand: 'Say "hi"' })],
    ['b=body !matches "^err"', row({ name: 'b', subject: 'body', op: '!matches', operand: '^err' })],
  ])('reads %s into a row and composes it back', (entry, want) => {
    expect(rowFromEntry(entry)).toEqual(want);
    expect(composeRow(want)).toBe(entry);
  });

  it('composes the canonical text the server stores', () => {
    expect(rowFromEntry('ok=status    ==   +200.50 @crit')).toEqual(row({ name: 'ok', op: '==', operand: '+200.50' }));
    expect(composeRow(rowFromEntry('ok=status    ==   +200.50 @crit') as AssertionRow)).toBe('ok=status == 200.5');
    expect(composeRow(row({ name: ' ok ', op: 'in', operand: '-0', hi: '1.0' }))).toBe('ok=status in 0..1');
    expect(composeExpression(row({ subject: 'cert_issuer', op: '==', operand: "Let's Encrypt" }))).toBe('cert_issuer == "Let\'s Encrypt"');
    expect(canonicalNumber('1e3')).toBe('1e3');
    expect(canonicalNumber(' 007 ')).toBe('7');
  });

  it('leaves blank rows out of the text and reads every line back', () => {
    const rows = [row({ name: 'a', operand: '200' }), emptyRow(), row({ name: 'b', subject: 'body', op: 'contains', operand: 'ok', severity: 'warn' })];
    const text = composeRows(rows);
    expect(text).toBe('a=status == 200\nb=body contains "ok" @warn');
    expect(parseRows(`${text}\n\n`)).toEqual({ rows: [rows[0], rows[2]] });
    expect(parseRows('a=status == 200\nnot an assertion')).toEqual({ bad: 2 });
  });

  it('keeps the operator and argument a new subject still takes', () => {
    const header = row({ name: 'h', subject: 'header', arg: 'Server', op: 'exists' });
    expect(withSubject(header, 'json')).toMatchObject({ subject: 'json', arg: 'Server', op: 'exists' });
    expect(withSubject(header, 'status')).toMatchObject({ subject: 'status', arg: '', op: '<' });
    const html = row({ subject: 'html', arg: 'p', op: '==', operand: 'x' });
    expect(withSubject(html, 'html count')).toMatchObject({ arg: 'p', op: '==', operand: 'x' });
    expect(withSubject(row({ op: 'in', operand: '1', hi: '2' }), 'body')).toMatchObject({ op: '==', operand: '1', hi: '' });
  });

  it('drops the operand of exists', () => {
    expect(withOp(row({ subject: 'header', arg: 'X', op: '==', operand: 'v' }), 'exists')).toMatchObject({ op: 'exists', operand: '' });
  });

  it('finds the entry an API error names', () => {
    const entries = ['ok=status == 200', 'a=body matches "("', 'b=html("p") exists'];
    expect(entryNamedBy(entries, 'http: assertions must be name=assertion such as ok=status == 200 (see the docs): "a=body matches \\"(\\"": the regular expression does not compile')).toBe(1);
    expect(entryNamedBy(entries, 'http: assertion "b" reads the body and needs a method other than HEAD: a HEAD response has no body')).toBe(2);
    expect(entryNamedBy(entries, 'http: json_values and assertions both name "ok"')).toBe(0);
    expect(entryNamedBy(entries, 'http: json_values names "ok" twice')).toBe(-1);
    expect(entryNamedBy(entries, 'http: url must be an http:// URL')).toBe(-1);
    expect(entryNamedBy(entries, null)).toBe(-1);
  });
});
