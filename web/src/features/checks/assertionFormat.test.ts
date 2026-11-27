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
      numeric: true,
    });
    expect(parseAssertion('rows=html("tr.item") count >= 2')).toEqual({
      name: 'rows',
      subject: 'html count',
      arg: 'tr.item',
      op: '>=',
      operand: '2',
      severity: 'crit',
      numeric: true,
    });
    expect(parseAssertion('ct=header("Content-Type") contains "json" @crit')).toEqual({
      name: 'ct',
      subject: 'header',
      arg: 'Content-Type',
      op: 'contains',
      operand: '"json"',
      severity: 'crit',
      numeric: false,
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

describe('the snmp dialect (backlog 3.134)', () => {
  it('reads value("name") as a number or a text comparison by its operator and operand', () => {
    expect(parseAssertion('busy=value("ups_load") < 80', 'snmp')).toMatchObject({ subject: 'value', arg: 'ups_load', op: '<', operand: '80', numeric: true });
    expect(parseAssertion('s=value("state") == 2', 'snmp')).toMatchObject({ op: '==', operand: '2', numeric: true });
    expect(parseAssertion('s=value("state") == "online" @warn', 'snmp')).toMatchObject({ operand: '"online"', numeric: false, severity: 'warn' });
    expect(parseAssertion('d=value("descr") !matches "^Cisco"', 'snmp')).toMatchObject({ op: '!matches', numeric: false });
    expect(parseAssertion('u=uptime > 600', 'snmp')).toMatchObject({ subject: 'uptime', numeric: true });
    expect(parseAssertion('r=response_ms in 0..100', 'snmp')).toMatchObject({ subject: 'response_ms', numeric: true });
  });

  it('refuses what the server refuses', () => {
    for (const entry of [
      's=value("state") == up',
      's=value("state") < "2"',
      's=value("state") contains 2',
      's=value("state") exists',
      's=value("a b") < 2',
      's=value("") < 2',
      's=value < 2',
      'u=uptime == "5"',
      'ok=status == 200',
    ]) {
      expect(parseAssertion(entry, 'snmp'), entry).toBeNull();
    }
    expect(parseAssertion('busy=value("ups_load") < 80')).toBeNull();
  });

  it('knows the snmp subjects and value\'s operators', () => {
    expect(isAssertionPlugin('snmp')).toBe(true);
    expect(dialectSubjects('snmp')).toEqual(['value', 'response_ms', 'uptime']);
    expect(assertionOps('value')).toEqual(['<', '<=', '>', '>=', '==', '!=', 'in', 'contains', '!contains', 'matches', '!matches']);
    expect(isNumericSubject('value')).toBe(false);
    expect(isNumericSubject('uptime')).toBe(true);
  });
});
