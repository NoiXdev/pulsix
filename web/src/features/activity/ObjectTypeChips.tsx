import { useT } from '../../i18n/useT.ts';
import { FilterChip } from '../../ui/list-toolbar.tsx';
import { OBJECT_TYPES, known } from './objectTypes.ts';

/** The object-type filter of the activity feeds: "all" and one chip per type (aria-pressed). */
export function ObjectTypeChips({ value, onChange }: { value: string; onChange: (type: string) => void }) {
  const t = useT();
  return (
    <>
      <FilterChip pressed={value === ''} onToggle={() => onChange('')}>
        {t.activity.allTypes}
      </FilterChip>
      {OBJECT_TYPES.map((type) => (
        <FilterChip key={type} pressed={value === type} onToggle={() => onChange(value === type ? '' : type)}>
          {known(t.activity.objectTypes, type)}
        </FilterChip>
      ))}
    </>
  );
}
