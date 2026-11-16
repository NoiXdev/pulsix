import type { RunPicture } from '../../api/hooks/useRunRequests.ts';

export type PictureFollowUps = NonNullable<RunPicture['follow_ups']>;
export type PictureFollowUp = NonNullable<PictureFollowUps['templates']>[number];

/** startableFollowUps are the direct follow-ups an approval can start: none
 * unless the organization lets approvals start them and the picture shows the
 * whole tree; a stop-marked one never starts. */
export function startableFollowUps(followUps: PictureFollowUps | undefined): PictureFollowUp[] {
  if (!followUps || !followUps.start || followUps.truncated) return [];
  return (followUps.templates ?? []).filter((f) => !f.stop);
}
