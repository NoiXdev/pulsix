import { useT } from '../../i18n/useT.ts';
import { OBJECT_TYPES, known } from './objectTypes.ts';

/** The object-type filter of the activity feeds: "all" and one chip per type. */
export function ObjectTypeChips({ value, onChange }: { value: string; onChange: (type: string) => void }) {
  const t = useT();
  return (
    <>
      <Chip label={t.activity.allTypes} on={value === ''} onClick={() => onChange('')} />
      {OBJECT_TYPES.map((type) => (
        <Chip key={type} label={known(t.activity.objectTypes, type)} on={value === type} onClick={() => onChange(type)} />
      ))}
    </>
  );
}

function Chip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        on
          ? 'flex h-[30px] items-center rounded-md border border-brand/40 bg-brand/10 px-3 text-xs font-medium text-brand'
          : 'flex h-[30px] items-center rounded-md border border-border bg-card px-3 text-xs font-medium text-muted-foreground'
      }
    >
      {label}
    </button>
  );
}
