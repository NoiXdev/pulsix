import { useCallback, useState } from 'react';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useSaveBackupSettings } from '../../api/hooks/useBackups.ts';
import type { BackupSettings, BackupSettingsInput } from '../../api/types.ts';
import { BackupDestinationCard } from './BackupDestinationCard.tsx';
import { BackupScheduleCard } from './BackupScheduleCard.tsx';

/**
 * BackupSettingsForm is the destination and the schedule-and-retention
 * cards. Each is its own form with its own save bar; one PUT still takes
 * the whole settings object, so a card sends its fields on top of the last
 * saved values of the other (backupDraft.toBody) and never the other
 * card's unsaved draft. The two share one save: while either card's PUT is
 * in flight both Save buttons are off, so a second PUT cannot overtake the
 * first with stale values. Leaving the page asks once if either is dirty.
 */
export function BackupSettingsForm({ settings }: { settings: BackupSettings }) {
  const [destinationDirty, setDestinationDirty] = useState(false);
  const [scheduleDirty, setScheduleDirty] = useState(false);
  useUnsavedGuard(destinationDirty || scheduleDirty);
  const mutation = useSaveBackupSettings();
  const { mutateAsync, reset } = mutation;
  const save = useCallback(
    async (body: BackupSettingsInput) => {
      try {
        await mutateAsync(body);
      } finally {
        // The variables may carry the S3 secret key: drop them at once.
        reset();
      }
    },
    [mutateAsync, reset],
  );
  return (
    <>
      <BackupDestinationCard settings={settings} onSave={save} saving={mutation.isPending} onDirtyChange={setDestinationDirty} />
      <BackupScheduleCard settings={settings} onSave={save} saving={mutation.isPending} onDirtyChange={setScheduleDirty} />
    </>
  );
}
