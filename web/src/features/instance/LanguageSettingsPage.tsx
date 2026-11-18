import { CardSaveBar, SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useMe } from '../../api/hooks/useMe.ts';
import { useInstanceLocale, useSaveInstanceLocale } from '../../api/hooks/useLocaleSettings.ts';
import { useT } from '../../i18n/useT.ts';
import { CATALOGUES } from '../../i18n/context.ts';
import { LOCALES, LOCALE_NAMES, isLocale, type Locale } from '../../i18n/types.ts';

/**
 * LanguageSettingsPage is the instance's default GUI language, for
 * instance administrators: GET/PUT /admin/settings/locale. The default is
 * what the sign-in pages are shown in and what every user without a
 * language of their own sees. Like the mail settings it is filed under an
 * organization's Settings only for navigation; it is instance-wide, which
 * the page says.
 */
export function LanguageSettingsPage() {
  const t = useT();
  const toast = useToast();
  const me = useMe({ enabled: true });
  const settings = useInstanceLocale();
  const save = useSaveInstanceLocale();
  const form = useCardForm<Locale>(settings.data?.default_locale ?? 'en');

  const guard = useQueryGuard(settings);
  if (guard) return guard;
  if (!settings.data) return <QueryLoading />;

  const value = form.draft;
  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!form.dirty) return;
    try {
      await save.mutateAsync(value);
      form.markSaved(value);
      // The page may have just switched language (an administrator without
      // a language of their own follows the default), so the confirmation
      // is said in the language the page is in now, not the one it was in.
      const own = me.data?.locale;
      toast.show(CATALOGUES[isLocale(own) ? own : value].language.instance.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.language.instance.saveFailed, 'destructive');
    }
  }

  return (
    <SettingsPage title={t.language.instance.title} description={t.language.instance.hint}>
      <form id="instance-language-form" onSubmit={(e) => void onSubmit(e)}>
        <SettingsCard
          title={t.language.label}
          description={t.language.instance.description}
          footer={
            <CardSaveBar
              formId="instance-language-form"
              dirty={form.dirty}
              saving={save.isPending}
              onDiscard={form.discard}
              saveLabel={t.language.instance.save}
            />
          }
        >
          <SettingsRow label={t.language.instance.field} htmlFor="instance_locale">
            <div className="w-60">
              <Select
                id="instance_locale"
                portal
                options={LOCALES.map((l) => ({ value: l, label: LOCALE_NAMES[l] }))}
                value={value}
                onChange={(v) => {
                  if (isLocale(v)) form.setDraft(v);
                }}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </form>
    </SettingsPage>
  );
}
