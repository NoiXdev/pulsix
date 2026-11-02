import { useState } from 'react';
import { Card } from '../../ui/table.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { useSetupStatus } from '../../api/hooks/useSetupStatus.ts';
import { useLocale, useT } from '../../i18n/useT.ts';
import { LOCALES, LOCALE_NAMES, isLocale } from '../../i18n/types.ts';

/** The Select's value for "no language of my own". */
const INSTANCE_DEFAULT = 'default';

/**
 * LanguageCard is the account page's language choice: the instance
 * default, or one of the languages for this user on every device (PUT
 * /auth/me/locale through useLocale().setLocale, which switches the whole
 * shell as soon as the server has it). The instance default's name comes
 * from the public setup status; while that is unknown the row just says
 * "Instance default".
 */
export function LanguageCard() {
  const t = useT();
  const toast = useToast();
  const { setLocale } = useLocale();
  const me = useMe({ enabled: true });
  const status = useSetupStatus();
  const [saving, setSaving] = useState(false);

  const instanceDefault = status.data?.default_locale;
  const own = me.data?.locale;
  const options = [
    {
      value: INSTANCE_DEFAULT,
      label: isLocale(instanceDefault)
        ? t.language.instanceDefault(LOCALE_NAMES[instanceDefault])
        : t.language.instanceDefaultUnknown,
    },
    ...LOCALES.map((l) => ({ value: l, label: LOCALE_NAMES[l] })),
  ];

  async function choose(value: string) {
    setSaving(true);
    try {
      await setLocale(isLocale(value) ? value : null);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.language.account.saveFailed, 'destructive');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <h2 className="text-sm font-semibold">{t.language.label}</h2>
        <Select
          aria-label={t.language.label}
          options={options}
          value={isLocale(own) ? own : INSTANCE_DEFAULT}
          onChange={(v) => void choose(v)}
          disabled={saving || !me.data}
        />
        <p className="text-[11px] text-muted-foreground">{t.language.account.hint}</p>
      </div>
    </Card>
  );
}
