import { SettingsCard } from '../../ui/settings.tsx';
import { useT } from '../../i18n/useT.ts';
import { AssistantRows } from './AssistantCard.tsx';
import { PersonalTokensRow } from './PersonalTokensCard.tsx';
import { ReadOnlyNote } from './ReadOnlyNote.tsx';

/**
 * OrgAccessCard is the "Assistant and token access" card of Settings →
 * General: the organization's switches for assistant access, the follow-ups
 * of approved runs and personal tokens. Each applies at once, so the card
 * has no save bar; without org.settings.write they are shown disabled.
 */
export function OrgAccessCard({ org, canWrite }: { org: string; canWrite: boolean }) {
  const t = useT();
  const a = t.settings.access;
  return (
    <SettingsCard title={a.title} description={a.description} footer={canWrite ? null : <ReadOnlyNote>{a.readOnlyHint}</ReadOnlyNote>}>
      <AssistantRows org={org} canWrite={canWrite} />
      <PersonalTokensRow org={org} canWrite={canWrite} />
    </SettingsCard>
  );
}
