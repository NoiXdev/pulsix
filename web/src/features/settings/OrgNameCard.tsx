import { useEffect } from 'react';
import { useUpdateOrg } from '../../api/hooks/useOrgs.ts';
import { Input } from '../../ui/input.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
import { useT } from '../../i18n/useT.ts';
import { ReadOnlyNote } from './ReadOnlyNote.tsx';

/**
 * OrgNameCard is the Organization card of Settings → General: the name,
 * which an owner or admin (org.manage) may change, and the slug, which is
 * fixed because every URL of the organization carries it. Anyone else sees
 * both read-only, without a save bar.
 */
export function OrgNameCard({
  org,
  name,
  slug,
  canManage,
  onDirtyChange,
}: {
  org: string;
  name: string;
  slug: string;
  canManage: boolean;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const t = useT();
  const g = t.settings.general;
  const toast = useToast();
  const update = useUpdateOrg(org);
  const form = useCardForm(name, { guard: false });
  const { dirty } = form;
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const next = form.draft.trim();
    if (!canManage || !dirty || next === '') return;
    try {
      await update.mutateAsync(next);
      form.markSaved(next);
      toast.show(g.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : g.saveFailed, 'destructive');
    }
  }

  return (
    <form id="org-name-form" onSubmit={(e) => void onSubmit(e)}>
      <SettingsCard
        title={g.card}
        footer={
          canManage ? (
            <CardSaveBar formId="org-name-form" dirty={dirty} saving={update.isPending} onDiscard={form.discard} saveLabel={g.save} />
          ) : (
            <ReadOnlyNote>{g.readOnlyHint}</ReadOnlyNote>
          )
        }
      >
        <SettingsRow label={g.name} htmlFor="org_name">
          <Input id="org_name" className="w-72 max-w-full" value={form.draft} readOnly={!canManage} onChange={(e) => form.setDraft(e.target.value)} />
        </SettingsRow>
        <SettingsRow label={g.slug} htmlFor="org_slug" help={g.slugHint}>
          <Input id="org_slug" className="w-72 max-w-full font-mono" value={slug} readOnly />
        </SettingsRow>
      </SettingsCard>
    </form>
  );
}
