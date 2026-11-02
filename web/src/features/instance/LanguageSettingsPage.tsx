import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Languages } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
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
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const me = useMe({ enabled: true });
  const settings = useInstanceLocale();
  const save = useSaveInstanceLocale();
  const [chosen, setChosen] = useState<Locale | null>(null);

  const guard = useQueryGuard(settings);
  if (guard) return guard;
  if (!settings.data) return <QueryLoading />;

  const value = chosen ?? settings.data.default_locale;
  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">{t.language.instance.title}</h1>
        <SettingsTabs org={org} />
      </div>
      <span className="text-[13px] text-muted-foreground">{t.language.instance.hint}</span>

      <div className="grid grid-cols-2 items-start gap-4">
        <Card>
          <form
            className="flex flex-col gap-3 p-4"
            onSubmit={async (e) => {
              e.preventDefault();
              try {
                await save.mutateAsync(value);
                setChosen(null);
                // The page may have just switched language (an
                // administrator without a language of their own follows
                // the default), so the confirmation is said in the
                // language the page is in now, not the one it was in.
                const own = me.data?.locale;
                toast.show(CATALOGUES[isLocale(own) ? own : value].language.instance.saved);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.language.instance.saveFailed, 'destructive');
              }
            }}
          >
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="instance_locale">{t.language.instance.field}</Label>
              <Select
                id="instance_locale"
                options={LOCALES.map((l) => ({ value: l, label: LOCALE_NAMES[l] }))}
                value={value}
                onChange={(v) => {
                  if (isLocale(v)) setChosen(v);
                }}
              />
            </div>
            <div className="flex justify-end">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                <Languages size={14} /> {t.language.instance.save}
              </Button>
            </div>
          </form>
        </Card>
      </div>
    </>
  );
}
