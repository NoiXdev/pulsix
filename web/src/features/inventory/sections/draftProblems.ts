import type { Problem } from '../../variables/types.ts';
import type { DraftMark, SaveTicket } from './useRevisionDraft.ts';

/** Reported is what was reported for one draft: the problems a save was
 * refused with (`answer` false), or the ones an answer that rebuilt the
 * draft brought (`answer` true). */
export interface Reported {
  readonly on: DraftMark;
  readonly problems: readonly Problem[];
  readonly answer: boolean;
}

const none: readonly Problem[] = [];

/** shownProblems is what a draft shows (backlog 7.91). With nothing
 * reported, the group's problems (`fromGroup`). Beside another generation
 * than the one reported for, none. A refusal describes the draft's own
 * content, so it shows whatever revision the draft has moved to since; an
 * answer describes what was stored at its revision, so it shows only while
 * the draft is on that revision, and the group's problems after. */
export function shownProblems(reported: Reported | null, draft: DraftMark, fromGroup: readonly Problem[]): readonly Problem[] {
  if (reported === null) return fromGroup;
  if (reported.on.generation !== draft.generation) return none;
  if (!reported.answer || reported.on.revision === draft.revision) return reported.problems;
  return fromGroup;
}

/** refusedWith reports a refused save's problems, as a state updater: only
 * while what is reported is still that save's start (`on` is the ticket
 * itself). A rebuild, an answer or a newer save reported since wins. */
export function refusedWith(ticket: SaveTicket, problems: readonly Problem[]): (cur: Reported | null) => Reported | null {
  return (cur) => (cur?.on === ticket ? { on: ticket, problems, answer: false } : cur);
}
