import { identitiesForBody, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { sameValue } from '../variables/origin.ts';
import type { Existing } from './existing.ts';
import type { PlaybookChoice } from './PlaybookPicker.tsx';
import type { Scope } from './ScopePicker.tsx';

/** The settings a template form edits, as the person has them now. */
export interface SettingsDraft {
  name: string;
  playbook: PlaybookChoice;
  scope: Scope;
  limit: string;
  credentialId: string;
  vaultRows: readonly VaultIdentityRow[];
  followUps: readonly string[];
  agentEnrollment: boolean;
}

/**
 * settingsChanged says whether a template form's draft differs from the
 * template it was loaded from (`base`): what leaving the form would lose.
 * Values are compared, not identities: a vault identity row's key is the
 * form's own, and a repository playbook picked but not registered yet (a
 * path) is a change.
 */
export function settingsChanged(draft: SettingsDraft, base: Existing): boolean {
  return (
    draft.name !== base.name ||
    draft.playbook.playbookId !== base.playbookId ||
    draft.playbook.path !== '' ||
    !sameValue(draft.scope, base.scope) ||
    draft.limit !== base.limit ||
    draft.credentialId !== base.credentialId ||
    !sameValue(identitiesForBody(draft.vaultRows) ?? [], identitiesForBody(base.vaultIdentities) ?? []) ||
    !sameValue(draft.followUps, base.followUps) ||
    draft.agentEnrollment !== base.agentEnrollment
  );
}
