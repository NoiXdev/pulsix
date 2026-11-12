import { Field } from '../../ui/field.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useT } from '../../i18n/useT.ts';
import type { CheckPluginDescriptor, CheckPluginParam } from '../../api/types.ts';
import { FieldError } from '../instance/shared/formParts.tsx';
import { paramText, placeholderOf, takesEscapes, type ParamValues } from './paramForm.ts';

const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

/**
 * ParamFields edits a check's parameters, field by field as the plugin's
 * descriptor lists them (GET /check-plugins, backlog 3.54), for both the
 * organization's check dialog and the instance's shared check form. Each
 * type has one control: a list a multi-line textarea (one entry per
 * line), an integer a number input, a string a text input, an enum a
 * select, a bool a switch. A field the user never touched holds no value,
 * shows the server's default (a placeholder, a preselection, a switch's
 * position) and is not sent (paramsFrom).
 */
export function ParamFields({
  idPrefix,
  plugin,
  values,
  errors,
  disabled,
  onChange,
}: {
  /** Prefixes every input's id, so two forms never share one. */
  idPrefix: string;
  plugin: CheckPluginDescriptor;
  values: ParamValues;
  errors: Record<string, string>;
  disabled?: boolean;
  onChange: (values: ParamValues) => void;
}) {
  const t = useT();
  const set = (name: string, value: string | boolean) => onChange({ ...values, [name]: value });
  const text = (spec: CheckPluginParam) => paramText(t, plugin.name, spec.name);
  const label = (spec: CheckPluginParam) => text(spec)?.label ?? spec.name;
  const lists = plugin.params.filter((p) => p.type === 'list');
  const inputs = plugin.params.filter((p) => p.type !== 'list' && p.type !== 'bool');
  const switches = plugin.params.filter((p) => p.type === 'bool');
  if (plugin.params.length === 0 && plugin.runs !== 'server') return null;

  function control(spec: CheckPluginParam, id: string) {
    const value = values[spec.name];
    const placeholder = placeholderOf(spec, text(spec));
    switch (spec.type) {
      case 'integer':
        // No min/max/step: the browser's own bubble would preempt the
        // field error validateParams gives.
        return (
          <Input
            id={id}
            type="number"
            inputMode="numeric"
            disabled={disabled}
            placeholder={placeholder}
            value={typeof value === 'string' ? value : ''}
            onChange={(e) => set(spec.name, e.target.value)}
          />
        );
      case 'enum': {
        const options = spec.options ?? [];
        const labels = text(spec)?.options;
        const preselected = typeof spec.default === 'string' ? spec.default : (options[0] ?? '');
        return (
          <Select
            id={id}
            disabled={disabled}
            invalid={!!errors[spec.name]}
            options={options.map((o) => ({ value: o, label: labels?.[o] ?? o }))}
            value={typeof value === 'string' ? value : preselected}
            onChange={(v) => set(spec.name, v)}
          />
        );
      }
      default:
        return (
          <Input
            id={id}
            className="font-mono"
            // An escape is two characters for one byte sent: the byte
            // bound itself is validateParams'.
            maxLength={takesEscapes(plugin.name, spec.name) ? 1000 : 500}
            disabled={disabled}
            placeholder={placeholder}
            value={typeof value === 'string' ? value : ''}
            onChange={(e) => set(spec.name, e.target.value)}
          />
        );
    }
  }

  return (
    <div className="flex flex-col gap-3">
      {plugin.runs === 'server' && <p className="text-[11px] text-muted-foreground">{t.checks.synthetic.runsFromServer}</p>}
      {lists.map((spec) => {
        const id = `${idPrefix}-${spec.name}`;
        const value = values[spec.name];
        const hint = text(spec)?.hint;
        return (
          <div key={spec.name} className="flex flex-col gap-1">
            <Label htmlFor={id}>{label(spec)}</Label>
            <textarea
              id={id}
              className={TEXTAREA}
              value={typeof value === 'string' ? value : ''}
              disabled={disabled}
              placeholder={placeholderOf(spec, text(spec))}
              aria-invalid={errors[spec.name] ? true : undefined}
              aria-describedby={hint ? `${id}-hint` : undefined}
              onChange={(e) => set(spec.name, e.target.value)}
            />
            {hint && (
              <p id={`${id}-hint`} className="text-[11px] text-muted-foreground">
                {hint}
              </p>
            )}
            <FieldError>{errors[spec.name]}</FieldError>
          </div>
        );
      })}
      {inputs.length > 0 && (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {inputs.map((spec) => {
            const id = `${idPrefix}-${spec.name}`;
            return (
              <Field key={spec.name} id={id} label={label(spec)} hint={text(spec)?.hint} error={errors[spec.name]}>
                {control(spec, id)}
              </Field>
            );
          })}
        </div>
      )}
      {switches.map((spec) => {
        const id = `${idPrefix}-${spec.name}`;
        const value = values[spec.name];
        return (
          <div key={spec.name} className="flex items-center gap-2 text-xs">
            <Switch
              id={id}
              disabled={disabled}
              checked={typeof value === 'boolean' ? value : spec.default === true}
              onCheckedChange={(checked) => set(spec.name, checked)}
            />
            <label htmlFor={id}>{label(spec)}</label>
          </div>
        );
      })}
    </div>
  );
}

/** ServerKeyHint sits under a check's key field while the plugin runs on
 * the Pulsix server: alert rules match these metrics only on the check
 * whose key is the plugin name. */
export function ServerKeyHint({ id, plugin }: { id: string; plugin: string }) {
  const t = useT();
  return (
    <p id={id} className="text-[11px] text-muted-foreground">
      {t.checks.synthetic.rulesKey(plugin)}
    </p>
  );
}

/** PluginsPending stands in for a check form until GET /check-plugins
 * has answered: loading, or the failure that keeps the form away. */
export function PluginsPending({ failed, error }: { failed: boolean; error?: unknown }) {
  const t = useT();
  if (failed) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {error instanceof Error && error.message ? `${t.checks.pluginsLoadFailed} ${error.message}` : t.checks.pluginsLoadFailed}
      </p>
    );
  }
  return <p className="text-xs text-muted-foreground">{t.checks.pluginsLoading}</p>;
}
