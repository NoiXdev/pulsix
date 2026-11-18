import { useState } from 'react';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import type { BackupSettings } from '../../api/types.ts';
import { BackupDestinationCard } from './BackupDestinationCard.tsx';
import { BackupScheduleCard } from './BackupScheduleCard.tsx';

/**
 * BackupSettingsForm is the destination and the schedule-and-retention
 * cards. Each is its own form with its own save bar; one PUT still takes
 * the whole settings object, so a card sends its fields on top of the last
 * saved values of the other (backupDraft.toBody) and never the other
 * card's unsaved draft. Leaving the page asks once if either is dirty.
 */
export function BackupSettingsForm({ settings }: { settings: BackupSettings }) {
  const [destinationDirty, setDestinationDirty] = useState(false);
  const [scheduleDirty, setScheduleDirty] = useState(false);
  useUnsavedGuard(destinationDirty || scheduleDirty);
  return (
    <>
      <BackupDestinationCard settings={settings} onDirtyChange={setDestinationDirty} />
      <BackupScheduleCard settings={settings} onDirtyChange={setScheduleDirty} />
    </>
  );
}
