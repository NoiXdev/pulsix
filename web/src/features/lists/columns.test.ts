import { describe, expect, it } from 'vitest';
import type { DataListColumn } from '../../ui/data-list/index.ts';
import { mergeColumns } from './columns.ts';

type Row = { id: string };

function col(id: string, extra: Partial<DataListColumn<Row>> = {}): DataListColumn<Row> {
  return { id, label: id, width: '1fr', defaultVisible: true, render: () => null, ...extra };
}

// The state dot is defined before the name column, as the hosts list has it
// today: the required column still comes first.
const COLUMNS = [col('state', { label: '' }), col('name', { required: true }), col('zone'), col('tags'), col('os', { defaultVisible: false })];

describe('mergeColumns', () => {
  it('returns the default columns, required first, when nothing is saved', () => {
    expect(mergeColumns(undefined, COLUMNS)).toEqual(['name', 'state', 'zone', 'tags']);
  });

  it('treats a saved null like nothing saved', () => {
    expect(mergeColumns(null, COLUMNS)).toEqual(['name', 'state', 'zone', 'tags']);
  });

  it('drops unknown and repeated ids and puts the required column first', () => {
    expect(mergeColumns(['zone', 'name', 'gone', 'zone'], COLUMNS)).toEqual(['name', 'zone']);
  });

  it('adds the required column when the saved list leaves it out', () => {
    expect(mergeColumns(['os', 'zone'], COLUMNS)).toEqual(['name', 'os', 'zone']);
  });

  it('keeps only the required column for an empty saved list', () => {
    expect(mergeColumns([], COLUMNS)).toEqual(['name']);
  });

  it('keeps the saved order for a list without a required column', () => {
    const plain = [col('a'), col('b'), col('c', { defaultVisible: false })];
    expect(mergeColumns(['c', 'a'], plain)).toEqual(['c', 'a']);
    expect(mergeColumns(undefined, plain)).toEqual(['a', 'b']);
  });
});
