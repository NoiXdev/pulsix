import type { ReactNode } from 'react';
import { SettingsCard } from '../../ui/settings.tsx';
import { useT } from '../../i18n/useT.ts';

/** DangerZone is the last card of the member and account pages: the
 * destructive actions, each a row whose button opens a confirmation. */
export function DangerZone({ children }: { children: ReactNode }) {
  const a = useT().users.actions;
  return (
    <SettingsCard title={a.dangerTitle} description={a.dangerDescription}>
      {children}
    </SettingsCard>
  );
}
