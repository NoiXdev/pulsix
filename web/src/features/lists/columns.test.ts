import { describe, expect, it } from 'vitest';
import type { DataListColumn } from '../../ui/data-list/index.ts';
import { mergeColumns } from './columns.ts';

type Row = { id: string };

function col(id: string, extra: Partial<DataListColumn<Row>> = {}): DataListColumn<Row> {
  return { id, label: id, width: '1fr', defaultVisible: true, render: () => null, ...extra };
}

// The state dot is defined before the name column, as the hosts list has it
// today: the required column still comes first.
const ALL = ['state', 'name', 'zone', 'tags', 'os'];
const COLUMNS = [col('state', { label: '' }), col('name', { required: true }), col('zone'), col('tags'), col('os', { defaultVisible: false })];

describe('mergeColumns', () => {
  it('returns the default columns, required first, when nothing is saved', () => {
    expect(mergeColumns(undefined, COLUMNS)).toEqual(['name', 'state', 'zone', 'tags']);
  });

  it('treats a saved null like nothing saved', () => {
    expect(mergeColumns(null, COLUMNS)).toEqual(['name', 'state', 'zone', 'tags']);
  });

  it('drops unknown and repeated ids and puts the required column first', () => {
    expect(mergeColumns({ columns: ['zone', 'name', 'gone', 'zone'], known: ALL }, COLUMNS)).toEqual(['name', 'zone']);
  });

  it('adds the required column when the saved list leaves it out', () => {
    expect(mergeColumns({ columns: ['os', 'zone'], known: ALL }, COLUMNS)).toEqual(['name', 'os', 'zone']);
  });

  it('keeps only the required column for an empty saved list', () => {
    expect(mergeColumns({ columns: [], known: ALL }, COLUMNS)).toEqual(['name']);
  });

  it('keeps the saved order for a list without a required column', () => {
    const plain = [col('a'), col('b'), col('c', { defaultVisible: false })];
    expect(mergeColumns({ columns: ['c', 'a'], known: ['a', 'b', 'c'] }, plain)).toEqual(['c', 'a']);
    expect(mergeColumns(undefined, plain)).toEqual(['a', 'b']);
  });

  describe('columns added since the save (known)', () => {
    const saved = { columns: ['zone', 'tags'], known: ['state', 'name', 'zone', 'tags', 'os'] };

    it('keeps a known column the saved list leaves out hidden', () => {
      expect(mergeColumns(saved, COLUMNS)).toEqual(['name', 'zone', 'tags']);
    });

    it('shows a new default-visible column at its default position', () => {
      const cols = [...COLUMNS.slice(0, 3), col('owner'), ...COLUMNS.slice(3)];
      expect(mergeColumns(saved, cols)).toEqual(['name', 'zone', 'owner', 'tags']);
    });

    it('puts a new first default column right after the required one', () => {
      const cols = [col('owner'), ...COLUMNS];
      expect(mergeColumns(saved, cols)).toEqual(['name', 'owner', 'zone', 'tags']);
    });

    it('does not show a new column that is not default-visible', () => {
      expect(mergeColumns(saved, [...COLUMNS, col('extra', { defaultVisible: false })])).toEqual(['name', 'zone', 'tags']);
    });

    it('keeps a saved choice without known as it is: every offered column counts as known', () => {
      expect(mergeColumns({ columns: ['zone'] }, COLUMNS)).toEqual(['name', 'zone']);
      expect(mergeColumns({ columns: ['zone'] }, [col('zone'), col('extra', { defaultVisible: false })])).toEqual(['zone']);
    });
  });
});
