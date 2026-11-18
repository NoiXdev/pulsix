import { useState, type KeyboardEvent } from 'react';
import { Check, Plus, X } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Segmented } from '../../../ui/segmented.tsx';
import { cn } from '../../../lib/utils.ts';
import { ScalarInput } from '../../variables/SchemaForm.tsx';
import { FieldMessages } from '../../variables/inputs.tsx';
import type { SchemaField } from '../../variables/types.ts';
import { formatList, parseList, toggledDefault, withDefault, withOption, withoutOption } from './fieldEdits.ts';
import { useT } from '../../../i18n/useT.ts';

export interface DefaultControlProps {
  field: SchemaField;
  id: string;
  /** The server's problems at the field's default and options. */
  error?: string;
  optionsError?: string;
  onChange: (next: SchemaField) => void;
}

/**
 * DefaultControl edits a field's default with the control its type calls
 * for: yes/no for a bool, option chips for an enum (which edit the options
 * too), comma separated text for a list, a number or text input otherwise.
 * A secret, a repeater and an object take no default (the server refuses
 * one), so they get a line saying so instead.
 */
export function DefaultControl({ field, id, error, optionsError, onChange }: DefaultControlProps) {
  const f = useT().inventory.fields;
  switch (field.type) {
    case 'secret':
    case 'repeater':
    case 'object':
      return (
        <div className="flex flex-col gap-1">
          <span className="text-[11px] font-medium text-muted-foreground">{f.default}</span>
          <p className="text-xs text-muted-foreground">{field.type === 'secret' ? f.secretNote : f.noDefaultFor}</p>
        </div>
      );
    case 'bool':
      return (
        <div className="flex flex-col gap-1">
          <Segmented
            label={f.default}
            options={[
              { value: 'none', label: f.boolNone },
              { value: 'yes', label: f.boolYes },
              { value: 'no', label: f.boolNo },
            ]}
            value={field.default === true ? 'yes' : field.default === false ? 'no' : 'none'}
            onChange={(v) => onChange(withDefault(field, v === 'none' ? undefined : v === 'yes'))}
          />
          <FieldMessages error={error} />
        </div>
      );
    case 'enum':
      return <OptionChips field={field} error={error} optionsError={optionsError} onChange={onChange} />;
    case 'list':
      return <ListDefault field={field} id={id} error={error} onChange={onChange} />;
    default:
      return (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id}>{f.default}</Label>
          <ScalarInput id={id} field={{ ...field, default: undefined, required: false }} value={field.default} onChange={(d) => onChange(withDefault(field, d))} invalid={!!error} />
          <FieldMessages error={error} />
        </div>
      );
  }
}

/** OptionChips edits an enum's options as chips: a click on one makes it
 * the default (again: no default), ✕ removes it, "+ Option" types a new
 * one (Enter adds it and keeps typing, Escape stops). */
function OptionChips({ field, error, optionsError, onChange }: { field: SchemaField; error?: string; optionsError?: string; onChange: (next: SchemaField) => void }) {
  const f = useT().inventory.fields;
  const [adding, setAdding] = useState(false);
  const [text, setText] = useState('');
  const options = field.options ?? [];

  const commit = () => {
    if (text.trim() !== '') onChange(withOption(field, text));
    setText('');
  };
  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault();
      commit();
    } else if (e.key === 'Escape') {
      // Only this input: the dialog around it must not take the press.
      e.preventDefault();
      setText('');
      setAdding(false);
    }
  }

  return (
    <fieldset className="flex min-w-0 flex-col gap-1">
      <legend className="mb-1 text-[11px] font-medium text-muted-foreground">
        {f.options} · {f.default}
      </legend>
      <div className="flex flex-wrap items-center gap-1.5">
        {options.map((o) => {
          const isDefault = field.default === o;
          return (
            <span key={o} className={cn('inline-flex items-center rounded-full border text-xs', isDefault ? 'border-primary bg-primary/15' : 'border-border bg-background')}>
              <button type="button" aria-pressed={isDefault} onClick={() => onChange(toggledDefault(field, o))} className="inline-flex items-center gap-1 rounded-l-full py-0.5 pr-1 pl-2.5 font-mono hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
                {isDefault && <Check size={11} aria-hidden="true" />}
                {o}
              </button>
              <button type="button" aria-label={f.removeOption(o)} onClick={() => onChange(withoutOption(field, o))} className="rounded-r-full py-1 pr-2 pl-1 text-muted-foreground hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
                <X size={11} />
              </button>
            </span>
          );
        })}
        {adding ? (
          <Input
            autoFocus
            aria-label={f.newOption}
            className="h-7 w-36 font-mono"
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKeyDown}
            onBlur={() => {
              commit();
              setAdding(false);
            }}
          />
        ) : (
          <Button type="button" variant="ghost" className="h-7 px-2 text-xs" onClick={() => setAdding(true)}>
            <Plus size={12} /> {f.addOption}
          </Button>
        )}
      </div>
      <span className="text-[11px] text-muted-foreground">{f.optionsHint}</span>
      <FieldMessages error={optionsError} />
      <FieldMessages error={error} />
    </fieldset>
  );
}

/** ListDefault is a list's default typed as comma separated text. It keeps
 * its own text, so a comma being typed is not swallowed; the text follows
 * the value when the value changes to something it does not already say. */
function ListDefault({ field, id, error, onChange }: { field: SchemaField; id: string; error?: string; onChange: (next: SchemaField) => void }) {
  const f = useT().inventory.fields;
  const item = field.item?.type ?? 'string';
  const [text, setText] = useState(() => formatList(field.default));
  const [seen, setSeen] = useState(field.default);
  if (seen !== field.default) {
    setSeen(field.default);
    if (formatList(parseList(text, item)) !== formatList(field.default)) setText(formatList(field.default));
  }
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{f.default}</Label>
      <Input
        id={id}
        className="font-mono"
        value={text}
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-hint`}
        onChange={(e) => {
          setText(e.target.value);
          onChange(withDefault(field, parseList(e.target.value, item)));
        }}
      />
      <span id={`${id}-hint`} className="text-[11px] text-muted-foreground">
        {f.listHint}
      </span>
      <FieldMessages error={error} />
    </div>
  );
}
