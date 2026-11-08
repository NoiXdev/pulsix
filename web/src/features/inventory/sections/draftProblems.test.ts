import { describe, expect, it } from 'vitest';
import { refusedWith, shownProblems, type Reported } from './draftProblems.ts';
import type { SaveTicket } from './useRevisionDraft.ts';

const FROM_GROUP = [{ path: 'env', message: 'is required' }];
const MINE = [{ path: 'env', message: 'is taken' }];

describe('shownProblems', () => {
  it('shows the problems a draft was reported with, and none beside another draft', () => {
    const reported: Reported = { on: { generation: 1, revision: 4 }, problems: MINE };
    const cases: { name: string; reported: Reported | null; generation: number; revision: number; want: readonly unknown[] }[] = [
      { name: 'nothing reported: the group’s problems', reported: null, generation: 1, revision: 4, want: FROM_GROUP },
      { name: 'the draft they were reported for', reported, generation: 1, revision: 4, want: MINE },
      { name: 'another generation', reported, generation: 2, revision: 4, want: [] },
      { name: 'another revision', reported, generation: 1, revision: 5, want: [] },
    ];
    for (const c of cases) {
      expect(shownProblems(c.reported, { generation: c.generation, revision: c.revision }, FROM_GROUP), c.name).toEqual(c.want);
    }
  });
});

describe('refusedWith', () => {
  const ticket: SaveTicket = { edits: 0, generation: 0, revision: 3 };

  it('reports the problems on the draft the save was started on', () => {
    expect(refusedWith(ticket, MINE)({ on: ticket, problems: [] })).toEqual({ on: ticket, problems: MINE });
  });

  it('keeps what was reported since: a rebuild, an answer or a newer save', () => {
    // An equal mark from another save is not this save's.
    const other: Reported = { on: { edits: 0, generation: 0, revision: 3 } as SaveTicket, problems: [] };
    expect(refusedWith(ticket, MINE)(other)).toBe(other);
    expect(refusedWith(ticket, MINE)(null)).toBeNull();
  });
});
