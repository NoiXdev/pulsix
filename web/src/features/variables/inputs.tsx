import { useState } from 'react';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useT } from '../../i18n/useT.ts';

/** FieldMessages shows what belongs under one input of the field editor:
 * what the source says when the field differs from it, and the server's
 * problem at this attribute. */
export function FieldMessages({ error, note }: { error?: string; note?: string }) {
  return (
    <>
      {note && <span className="text-[11px] text-muted-foreground">{note}</span>}
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </>
  );
}

interface TextFieldProps {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  note?: string;
  mono?: boolean;
  readOnly?: boolean;
}

export function TextField({ id, label, value, onChange, error, note, mono, readOnly }: TextFieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        value={value}
        readOnly={readOnly}
        className={mono ? 'font-mono' : undefined}
        aria-invalid={error ? true : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      <FieldMessages error={error} note={note} />
    </div>
  );
}

interface NumberFieldProps {
  id: string;
  label: string;
  value?: number;
  onChange: (v: number | undefined) => void;
  error?: string;
  note?: string;
}

export function NumberField({ id, label, value, onChange, error, note }: NumberFieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="text"
        inputMode="decimal"
        className="font-mono"
        value={value === undefined ? '' : String(value)}
        aria-invalid={error ? true : undefined}
        onChange={(e) => {
          const raw = e.target.value.trim();
          const n = Number(raw);
          onChange(raw === '' || !Number.isFinite(n) ? undefined : n);
        }}
      />
      <FieldMessages error={error} note={note} />
    </div>
  );
}

interface OptionsFieldProps {
  id: string;
  value: string[];
  onChange: (v: string[]) => void;
  error?: string;
  note?: string;
}

/** OptionsField edits enum options one per line. It keeps its own text, so
 * an empty line being typed is not swallowed; the text follows the value
 * when the value changes to something the text does not already say (a
 * reset to the source). */
export function OptionsField({ id, value, onChange, error, note }: OptionsFieldProps) {
  const t = useT();
  const [text, setText] = useState(value.join('\n'));
  const [seen, setSeen] = useState(value);
  if (seen !== value) {
    setSeen(value);
    if (parseOptions(text).join('\n') !== value.join('\n')) setText(value.join('\n'));
  }
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{t.variables.builder.options}</Label>
      <textarea
        id={id}
        rows={4}
        className="rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground focus-visible:border-primary focus-visible:outline-none"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          onChange(parseOptions(e.target.value));
        }}
      />
      <FieldMessages error={error} note={note} />
    </div>
  );
}

function parseOptions(text: string): string[] {
  return text.split('\n').map((o) => o.trim()).filter((o) => o !== '');
}

interface SwitchFieldProps {
  id: string;
  label: string;
  checked: boolean;
  onChange: (c: boolean) => void;
  note?: string;
  disabled?: boolean;
}

export function SwitchField({ id, label, checked, onChange, note, disabled }: SwitchFieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2 text-xs">
        <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} />
        <label htmlFor={id}>{label}</label>
      </div>
      <FieldMessages note={note} />
    </div>
  );
}
