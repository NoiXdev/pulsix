import type { Problem } from '../../variables/types.ts';
import type { DraftMark, SaveTicket } from './useRevisionDraft.ts';

/** Reported is what was reported for one draft: the problems a save was
 * refused with, or the ones an answer that rebuilt the draft brought. */
export interface Reported {
  readonly on: DraftMark;
  readonly problems: readonly Problem[];
}

const none: readonly Problem[] = [];

/** shownProblems is what a draft shows (backlog 7.91): with nothing
 * reported, the problems the group came with (`fromGroup`), since the draft
 * was built from it; the problems reported for this very draft (same
 * generation and revision); none beside any other draft. */
export function shownProblems(reported: Reported | null, draft: DraftMark, fromGroup: readonly Problem[]): readonly Problem[] {
  if (reported === null) return fromGroup;
  return reported.on.generation === draft.generation && reported.on.revision === draft.revision ? reported.problems : none;
}

/** refusedWith reports a refused save's problems, as a state updater: only
 * while what is reported is still that save's start (`on` is the ticket
 * itself). A rebuild, an answer or a newer save reported since wins. */
export function refusedWith(ticket: SaveTicket, problems: readonly Problem[]): (cur: Reported | null) => Reported | null {
  return (cur) => (cur?.on === ticket ? { on: ticket, problems } : cur);
}
