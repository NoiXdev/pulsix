import { describe, expect, it } from 'vitest';
import { isTimeShape, joinDateTime, parseIsoDate, parseTime, splitDateTime, toIsoDate } from './pickerValue.ts';

describe('parseIsoDate', () => {
  it('reads a real day as local midnight', () => {
    expect(parseIsoDate('2026-11-11')).toEqual(new Date(2026, 10, 11));
  });

  it.each(['', '2026-02-30', '2026-13-01', '11.11.2026', '2026-11-11T10:00', undefined])('refuses %s', (v) => {
    expect(parseIsoDate(v)).toBeUndefined();
  });

  it('round-trips through toIsoDate', () => {
    expect(toIsoDate(parseIsoDate('2026-01-05')!)).toBe('2026-01-05');
  });
});

describe('parseTime', () => {
  it.each([
    ['0930', '09:30'],
    ['930', '09:30'],
    ['9:30', '09:30'],
    ['9.30', '09:30'],
    ['9:5', '09:05'],
    ['9', '09:00'],
    ['0', '00:00'],
    [' 23:59 ', '23:59'],
    ['00:00', '00:00'],
  ])('reads %s as %s', (text, want) => {
    expect(parseTime(text)).toBe(want);
  });

  it.each(['', '24:00', '25:00', '12:60', '1260', 'abc', '12345', '9:', ':30', '12:3a'])('refuses %s', (text) => {
    expect(parseTime(text)).toBeNull();
  });
});

describe('isTimeShape', () => {
  it.each([
    ['09:30', true],
    ['25:00', true],
    ['9:30', false],
    ['0930', false],
    ['09:3', false],
  ])('%s → %s', (text, want) => {
    expect(isTimeShape(text)).toBe(want);
  });
});

describe('splitDateTime and joinDateTime', () => {
  it('split a value into its halves and join them back', () => {
    expect(splitDateTime('2026-11-11T14:30')).toEqual({ date: '2026-11-11', time: '14:30' });
    expect(joinDateTime({ date: '2026-11-11', time: '14:30' })).toBe('2026-11-11T14:30');
  });

  it('split the empty value into empty halves', () => {
    expect(splitDateTime('')).toEqual({ date: '', time: '' });
  });

  it('join to the empty value while either half is missing', () => {
    expect(joinDateTime({ date: '2026-11-11', time: '' })).toBe('');
    expect(joinDateTime({ date: '', time: '14:30' })).toBe('');
  });
});
