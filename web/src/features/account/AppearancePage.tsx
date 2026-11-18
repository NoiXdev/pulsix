import { useRef } from 'react';
import { SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { Button } from '../../ui/button.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePreferences, useSavePreferences, type PreferencesUpdate } from '../../api/hooks/usePreferences.ts';
import type { Density, PageSize } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { LIST_COLUMNS, listName } from '../lists/registry.ts';
import { cn } from '../../lib/utils.ts';
import { LanguageCard } from './LanguageCard.tsx';

const DENSITIES: readonly Density[] = ['comfortable', 'standard', 'compact'];
const PAGE_SIZES: readonly PageSize[] = [10, 25, 50, 100];

/** The mini preview's bars: tighter density, more and thinner rows. */
const PREVIEW: Record<Density, { rows: number; height: number }> = {
  comfortable: { rows: 3, height: 20 },
  standard: { rows: 4, height: 15 },
  compact: { rows: 6, height: 10 },
};

/**
 * AppearancePage is Settings > Appearance: how lists look for this person
 * in every browser. The density and page size apply to every list; the
 * columns a list remembers are listed with a Reset each; the language card
 * moved here from the security page. Everything applies at once, so no
 * card has a save bar.
 */
export function AppearancePage() {
  const t = useT();
  const toast = useToast();
  const { prefs, loaded, ready } = usePreferences();
  const save = useSavePreferences();

  function change(update: PreferencesUpdate) {
    save(update).catch(() => toast.show(t.appearance.saveFailed, 'destructive'));
  }

  // Every list the app has, then any saved id this build does not know.
  const known = Object.keys(LIST_COLUMNS);
  const lists = [...known, ...Object.keys(prefs.lists).filter((id) => !known.includes(id))];

  function state(id: string): string {
    const columns = Object.hasOwn(prefs.lists, id) ? prefs.lists[id].columns : null;
    if (!columns) return t.appearance.columns.standard;
    const total = Object.hasOwn(LIST_COLUMNS, id) ? LIST_COLUMNS[id as keyof typeof LIST_COLUMNS].length : 0;
    return total > 0 ? t.appearance.columns.state(columns.length, total) : t.appearance.columns.own(columns.length);
  }

  return (
    <SettingsPage title={t.appearance.title} description={t.appearance.subtitle}>
      {ready && !loaded && (
        <p role="alert" className="rounded-md border border-destructive/40 px-3 py-2 text-xs text-destructive">
          {t.appearance.loadFailed}
        </p>
      )}
      <SettingsCard title={t.appearance.density.title} description={t.appearance.density.description}>
        <div className="border-t border-border px-5 py-4">
          <DensityPicker value={prefs.density} disabled={!loaded} onChange={(density) => change((p) => ({ ...p, density }))} />
        </div>
      </SettingsCard>

      <SettingsCard title={t.appearance.lists} description={t.appearance.listsDescription}>
        <SettingsRow label={t.appearance.pageSize.title} htmlFor="appearance-page-size">
          <div className="w-28">
            <Select
              id="appearance-page-size"
              options={PAGE_SIZES.map((n) => ({ value: String(n), label: String(n) }))}
              value={String(prefs.page_size)}
              onChange={(v) => change((p) => ({ ...p, page_size: Number(v) as PageSize }))}
              disabled={!loaded}
            />
          </div>
        </SettingsRow>
      </SettingsCard>

      <LanguageCard />

      <SettingsCard title={t.appearance.columns.title} description={t.appearance.columns.hint}>
        <ul>
          {lists.map((id) => {
            const own = Object.hasOwn(prefs.lists, id);
            return (
              <li key={id}>
                <SettingsRow label={listName(id, t)} help={state(id)}>
                  <Button
                    variant="default"
                    aria-label={t.appearance.columns.resetAria(listName(id, t))}
                    disabled={!own || !loaded}
                    onClick={() =>
                      change((p) => {
                        const next = { ...p.lists };
                        delete next[id];
                        return { ...p, lists: next };
                      })
                    }
                  >
                    {t.appearance.columns.reset}
                  </Button>
                </SettingsRow>
              </li>
            );
          })}
        </ul>
      </SettingsCard>
    </SettingsPage>
  );
}

/** Three cards as one radio group, each with a small preview of the rows. */
function DensityPicker({ value, disabled, onChange }: { value: Density; disabled: boolean; onChange: (d: Density) => void }) {
  const t = useT();
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});

  function onKeyDown(e: React.KeyboardEvent, i: number) {
    const step = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
    if (step === 0) return;
    e.preventDefault();
    const next = DENSITIES[(i + step + DENSITIES.length) % DENSITIES.length];
    refs.current[next]?.focus();
    onChange(next);
  }

  return (
    <div role="radiogroup" aria-label={t.appearance.density.title} className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
      {DENSITIES.map((d, i) => {
        const on = d === value;
        const hint = t.appearance.density[`${d}Hint`];
        return (
          <button
            key={d}
            ref={(el) => {
              refs.current[d] = el;
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            disabled={disabled}
            onClick={() => onChange(d)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={cn(
              'flex flex-col gap-2 rounded-lg border p-3 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-60',
              on ? 'border-brand/60 bg-brand/10' : 'border-border bg-card hover:border-brand-dim',
            )}
          >
            <b className="text-[13px] font-semibold">{t.appearance.density[d]}</b>
            <span aria-hidden="true" className="flex h-[66px] flex-col gap-[3px] overflow-hidden rounded border border-border bg-background p-1.5">
              {Array.from({ length: PREVIEW[d].rows }, (_, n) => (
                <i key={n} className="block shrink-0 rounded-[2px] bg-border" style={{ height: PREVIEW[d].height / 2.5 }} />
              ))}
            </span>
            <small className="text-[11px] text-muted-foreground">{hint}</small>
          </button>
        );
      })}
    </div>
  );
}
