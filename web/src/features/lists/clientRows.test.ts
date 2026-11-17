import { describe, expect, it } from 'vitest';
import type { DataListColumn } from '../../ui/data-list/index.ts';
import { pageRows, sortRows } from './clientRows.ts';

type Row = { id: string; name: string; seen?: number | null; on?: boolean; zone?: string };

const COLUMNS: DataListColumn<Row>[] = [
  { id: 'name', label: 'Name', width: '1fr', defaultVisible: true, required: true, sortKey: 'name', sortValue: (r) => r.name, render: (r) => r.name },
  { id: 'seen', label: 'Seen', width: '1fr', defaultVisible: true, sortKey: 'seen', sortValue: (r) => r.seen, render: () => null },
  { id: 'on', label: 'On', width: '1fr', defaultVisible: true, sortKey: 'on', sortValue: (r) => r.on, render: () => null },
  { id: 'zone', label: 'Zone', width: '1fr', defaultVisible: true, sortKey: 'zone', sortValue: (r) => r.zone, render: () => null },
];
const key = (r: Row) => r.id;
const ids = (rows: Row[]) => rows.map((r) => r.id);

describe('sortRows', () => {
  it('compares numbers inside names numerically', () => {
    const rows = [
      { id: 'a', name: 'host10' },
      { id: 'b', name: 'host9' },
      { id: 'c', name: 'Host1' },
    ];
    expect(ids(sortRows(rows, COLUMNS, { key: 'name', order: 'asc' }, key, 'en'))).toEqual(['c', 'b', 'a']);
    expect(ids(sortRows(rows, COLUMNS, { key: 'name', order: 'desc' }, key, 'en'))).toEqual(['a', 'b', 'c']);
  });

  it('puts missing values last in both directions', () => {
    const rows: Row[] = [
      { id: 'a', name: 'a', seen: null },
      { id: 'b', name: 'b', seen: 5 },
      { id: 'c', name: 'c' },
      { id: 'd', name: 'd', seen: 1 },
    ];
    expect(ids(sortRows(rows, COLUMNS, { key: 'seen', order: 'asc' }, key, 'en'))).toEqual(['d', 'b', 'a', 'c']);
    expect(ids(sortRows(rows, COLUMNS, { key: 'seen', order: 'desc' }, key, 'en'))).toEqual(['b', 'd', 'a', 'c']);
  });

  it('puts an empty string last too', () => {
    const rows: Row[] = [
      { id: 'a', name: 'a', zone: '' },
      { id: 'b', name: 'b', zone: 'west' },
      { id: 'c', name: 'c', zone: 'east' },
    ];
    expect(ids(sortRows(rows, COLUMNS, { key: 'zone', order: 'desc' }, key, 'en'))).toEqual(['b', 'c', 'a']);
  });

  it('breaks ties by the row key', () => {
    const rows: Row[] = [
      { id: 'z', name: 'z', on: true },
      { id: 'b', name: 'b', on: false },
      { id: 'a', name: 'a', on: true },
      { id: 'c', name: 'c', on: false },
    ];
    expect(ids(sortRows(rows, COLUMNS, { key: 'on', order: 'asc' }, key, 'en'))).toEqual(['b', 'c', 'a', 'z']);
    expect(ids(sortRows(rows, COLUMNS, { key: 'on', order: 'desc' }, key, 'en'))).toEqual(['a', 'z', 'b', 'c']);
  });

  it('leaves the order alone without a sort or for a key no column offers', () => {
    const rows: Row[] = [
      { id: 'b', name: 'b' },
      { id: 'a', name: 'a' },
    ];
    expect(ids(sortRows(rows, COLUMNS, null, key, 'en'))).toEqual(['b', 'a']);
    expect(ids(sortRows(rows, COLUMNS, { key: 'bogus', order: 'asc' }, key, 'en'))).toEqual(['b', 'a']);
  });

  it('does not change the rows it is given', () => {
    const rows: Row[] = [
      { id: 'b', name: 'b' },
      { id: 'a', name: 'a' },
    ];
    sortRows(rows, COLUMNS, { key: 'name', order: 'asc' }, key, 'en');
    expect(ids(rows)).toEqual(['b', 'a']);
  });
});

describe('pageRows', () => {
  const rows = Array.from({ length: 60 }, (_, i) => i + 1);

  it('returns the rows of one page', () => {
    expect(pageRows(rows, 1, 25)).toEqual(rows.slice(0, 25));
    expect(pageRows(rows, 2, 25)).toEqual(rows.slice(25, 50));
    expect(pageRows(rows, 2, 25)[0]).toBe(26);
    expect(pageRows(rows, 3, 25)).toEqual([51, 52, 53, 54, 55, 56, 57, 58, 59, 60]);
  });

  it('returns nothing past the end', () => {
    expect(pageRows(rows, 4, 25)).toEqual([]);
  });
});
