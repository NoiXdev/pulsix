import { Field } from '../../ui/field.tsx';
import { Input } from '../../ui/input.tsx';
import { Select } from '../../ui/Select.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useT } from '../../i18n/useT.ts';
import { isSynthetic, SYNTHETIC_FIELDS, type FieldPlugin, type SyntheticField, type SyntheticPlugin, type SyntheticValues } from './syntheticParams.ts';

/**
 * SyntheticParamFields edits the parameters of a check with a field table (backlog
 * 3.13) or a docker check's (3.12), for both the organization's check dialog and the instance's
 * shared check form. A field the user never touched holds no value, shows
 * the server's default and is not sent (syntheticParams).
 */
export function SyntheticParamFields({
  idPrefix,
  plugin,
  values,
  errors,
  disabled,
  onChange,
}: {
  /** Prefixes every input's id, so two forms never share one. */
  idPrefix: string;
  plugin: FieldPlugin;
  values: SyntheticValues;
  errors: Record<string, string>;
  disabled?: boolean;
  onChange: (values: SyntheticValues) => void;
}) {
  const t = useT();
  const s = t.checks.synthetic;
  const set = (name: string, value: string | boolean) => onChange({ ...values, [name]: value });
  const fields = SYNTHETIC_FIELDS[plugin];
  const inputs = fields.filter((f) => f.kind !== 'bool');
  const switches = fields.filter((f) => f.kind === 'bool');

  function control(field: SyntheticField, id: string) {
    const value = values[field.name];
    switch (field.kind) {
      case 'number':
        return (
          <Input
            id={id}
            type="number"
            inputMode="numeric"
            disabled={disabled}
            placeholder={field.placeholder}
            value={typeof value === 'string' ? value : ''}
            onChange={(e) => set(field.name, e.target.value)}
          />
        );
      case 'text':
        return (
          <Input
            id={id}
            className="font-mono"
            // An escape is two characters for one byte sent: the
            // byte bound itself is validateSynthetic's.
            maxLength={field.escapes ? 1000 : 500}
            disabled={disabled}
            placeholder={field.placeholder}
            value={typeof value === 'string' ? value : ''}
            onChange={(e) => set(field.name, e.target.value)}
          />
        );
      case 'select':
        return (
          <Select
            id={id}
            disabled={disabled}
            invalid={!!errors[field.name]}
            options={field.options.map((o) => ({ value: o, label: field.optionLabels ? s.options[field.optionLabels[o]] : o }))}
            value={typeof value === 'string' ? value : field.default}
            onChange={(v) => set(field.name, v)}
          />
        );
      default:
        return null;
    }
  }

  return (
    <div className="flex flex-col gap-3">
      {isSynthetic(plugin) && <p className="text-[11px] text-muted-foreground">{s.runsFromServer}</p>}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {inputs.map((field) => {
          const id = `${idPrefix}-${field.name}`;
          return (
            <Field key={field.name} id={id} label={s.fields[field.label]} hint={field.hint && s.hints[field.hint]} error={errors[field.name]}>
              {control(field, id)}
            </Field>
          );
        })}
      </div>
      {switches.map((field) => {
        if (field.kind !== 'bool') return null;
        const id = `${idPrefix}-${field.name}`;
        const value = values[field.name];
        return (
          <div key={field.name} className="flex items-center gap-2 text-xs">
            <Switch
              id={id}
              disabled={disabled}
              checked={typeof value === 'boolean' ? value : field.default}
              onCheckedChange={(checked) => set(field.name, checked)}
            />
            <label htmlFor={id}>{s.fields[field.label]}</label>
          </div>
        );
      })}
    </div>
  );
}

/** SyntheticKeyHint sits under a check's key field while the plugin is
 * synthetic: alert rules match these metrics only on the check whose key
 * is the plugin name. */
export function SyntheticKeyHint({ id, plugin }: { id: string; plugin: SyntheticPlugin }) {
  const t = useT();
  return (
    <p id={id} className="text-[11px] text-muted-foreground">
      {t.checks.synthetic.rulesKey(plugin)}
    </p>
  );
}
