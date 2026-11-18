import { useId } from 'react';
import { cn } from '../../../lib/utils.ts';
import { typesFor } from '../../variables/fieldEditor.ts';
import { FieldMessages } from '../../variables/inputs.tsx';
import type { FieldType } from '../../variables/types.ts';
import { TYPE_ICONS } from './typeIcons.ts';
import { useT } from '../../../i18n/useT.ts';

/** TypeCards picks a field's type from one card per type the schema has,
 * each with its icon and a line of help. Native radios underneath: the
 * arrow keys move between the cards. */
export function TypeCards({ value, error, onChange }: { value: FieldType; error?: string; onChange: (type: FieldType) => void }) {
  const f = useT().inventory.fields;
  const name = useId();
  return (
    <fieldset className="flex min-w-0 flex-col gap-1">
      <legend className="mb-1 text-[11px] font-medium text-muted-foreground">{f.type}</legend>
      <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3">
        {typesFor('group').map((type) => {
          const Icon = TYPE_ICONS[type];
          const checked = type === value;
          return (
            <label
              key={type}
              className={cn(
                'flex cursor-pointer items-start gap-2 rounded-md border px-2 py-1.5 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-primary has-[:disabled]:cursor-not-allowed',
                checked ? 'border-primary bg-primary/10' : 'border-border hover:border-primary/60',
              )}
            >
              <input type="radio" className="sr-only" name={name} value={type} checked={checked} onChange={() => onChange(type)} />
              <Icon size={14} aria-hidden="true" className={cn('mt-0.5 shrink-0', checked ? 'text-primary' : 'text-muted-foreground')} />
              <span className="flex min-w-0 flex-col">
                <span className="text-xs font-medium">{f.types[type].label}</span>{' '}
                <span className="text-[10px] leading-snug text-muted-foreground">{f.types[type].help}</span>
              </span>
            </label>
          );
        })}
      </div>
      <FieldMessages error={error} />
    </fieldset>
  );
}
