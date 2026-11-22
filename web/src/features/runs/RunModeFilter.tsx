import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * RunModeFilter is the runs lists' Dry runs filter: the dry runs only
 * (`check_mode=true`), the real runs only (false) or both (undefined).
 */
export function RunModeFilter({ value, onChange }: { value: boolean | undefined; onChange: (value: boolean | undefined) => void }) {
  const t = useT();
  const d = t.runs.dryRun;
  return (
    <FilterSelect
      label={d.filter}
      allLabel={d.all}
      options={[
        { value: 'true', label: d.dry },
        { value: 'false', label: d.real },
      ]}
      value={value === undefined ? undefined : String(value)}
      onChange={(v) => onChange(v === undefined ? undefined : v === 'true')}
    />
  );
}
