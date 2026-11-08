import { describe, expect, it } from 'vitest';
import { refusedWith, shownProblems, type Reported } from './draftProblems.ts';
import type { SaveTicket } from './useRevisionDraft.ts';

const FROM_GROUP = [{ path: 'env', message: 'is required' }];
const MINE = [{ path: 'env', message: 'is taken' }];

describe('shownProblems', () => {
  it('shows what was reported for a draft only beside that draft', () => {
    const refusal: Reported = { on: { generation: 1, revision: 4 }, problems: MINE, answer: false };
    const answer: Reported = { on: { generation: 1, revision: 4 }, problems: MINE, answer: true };
    const cases: { name: string; reported: Reported | null; generation: number; revision: number; want: readonly unknown[] }[] = [
      { name: 'nothing reported: the group’s problems', reported: null, generation: 1, revision: 4, want: FROM_GROUP },
      { name: 'a refusal, the draft it was reported for', reported: refusal, generation: 1, revision: 4, want: MINE },
      { name: 'a refusal, another generation', reported: refusal, generation: 2, revision: 4, want: [] },
      // The draft's content is unchanged: its revision moved along own steps.
      { name: 'a refusal, the same generation on another revision', reported: refusal, generation: 1, revision: 5, want: MINE },
      { name: 'an answer, the draft it rebuilt on its revision', reported: answer, generation: 1, revision: 4, want: MINE },
      { name: 'an answer, another generation', reported: answer, generation: 2, revision: 4, want: [] },
      // The answer no longer describes what is stored: the group does.
      { name: 'an answer, the same generation on another revision', reported: answer, generation: 1, revision: 5, want: FROM_GROUP },
    ];
    for (const c of cases) {
      expect(shownProblems(c.reported, { generation: c.generation, revision: c.revision }, FROM_GROUP), c.name).toEqual(c.want);
    }
  });
});

describe('refusedWith', () => {
  const ticket: SaveTicket = { edits: 0, generation: 0, revision: 3 };

  it('reports the problems on the draft the save was started on', () => {
    expect(refusedWith(ticket, MINE)({ on: ticket, problems: [], answer: false })).toEqual({ on: ticket, problems: MINE, answer: false });
  });

  it('keeps what was reported since: a rebuild, an answer or a newer save', () => {
    // An equal mark from another save is not this save's.
    const other: Reported = { on: { edits: 0, generation: 0, revision: 3 } as SaveTicket, problems: [], answer: false };
    expect(refusedWith(ticket, MINE)(other)).toBe(other);
    expect(refusedWith(ticket, MINE)(null)).toBeNull();
  });
});
