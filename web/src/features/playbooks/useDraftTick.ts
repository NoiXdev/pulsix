import { useState } from 'react';

/** useDraftTick is the Dry run form's "check the unsaved draft" box. It can
 * only be ticked while there is a draft to check (`can`); once there is not
 * (the draft was saved, or reverted), the tick is let go of, so a later draft
 * starts unticked instead of being checked unasked. */
export function useDraftTick(can: boolean): readonly [boolean, (on: boolean) => void] {
  const [on, setOn] = useState(false);
  if (!can && on) setOn(false);
  return [on && can, setOn];
}
