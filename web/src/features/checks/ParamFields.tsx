import { Field } from '../../ui/field.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useT } from '../../i18n/useT.ts';
import type { CheckPluginDescriptor, CheckPluginParam, MonitoringGroup } from '../../api/types.ts';
import { FieldError } from '../instance/shared/formParts.tsx';
import {
  changesState, isAssertions, isMultiline, paramText, placeholderOf, takesEscapes, VANTAGE_PARAMS, type ParamValues,
} from './paramForm.ts';
import { VantageFields } from './VantageFields.tsx';
import { AssertionEditor } from './AssertionEditor.tsx';
import { isAssertionPlugin } from './assertionFormat.ts';

const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

/**
 * ParamFields edits a check's parameters, field by field as the plugin's
 * descriptor lists them (GET /check-plugins, backlog 3.54), for both the
 * organization's check dialog and the instance's shared check form. Each
 * type has one control: a list a multi-line textarea (one entry per
 * line), an integer a number input, a string a text input (a multi-line
 * one, http's request body, a textarea), an enum a select, a bool a
 * switch. A field the user never touched holds no value,
 * shows the server's default (a placeholder, a preselection, a switch's
 * position) and is not sent (paramsFrom).
 *
 * A synthetic plugin's vantage points (backlog 3.13a) are their own
 * section (VantageFields) when the form knows the organization's
 * monitoring groups (`groups`), and left out when it does not — the
 * instance's shared check form, whose checks are probed from the server.
 */
export function ParamFields({
  idPrefix,
  plugin,
  values,
  errors,
  disabled,
  groups,
  serverError,
  onChange,
}: {
  /** Prefixes every input's id, so two forms never share one. */
  idPrefix: string;
  plugin: CheckPluginDescriptor;
  values: ParamValues;
  errors: Record<string, string>;
  disabled?: boolean;
  /** The organization's monitoring groups, for the vantage points; absent
   * leaves those parameters out. */
  groups?: readonly MonitoringGroup[];
  /** The API's refusal of the last save: the http check's assertion
   * editor shows it on the entry it names. */
  serverError?: string | null;
  onChange: (values: ParamValues) => void;
}) {
  const t = useT();
  const set = (name: string, value: string | boolean) => onChange({ ...values, [name]: value });
  const text = (spec: CheckPluginParam) => paramText(t, plugin.name, spec.name);
  const label = (spec: CheckPluginParam) => text(spec)?.label ?? spec.name;
  const own = plugin.params.filter((p) => !VANTAGE_PARAMS.has(p.name));
  // A multi-line string (http's body) is a textarea like a list.
  const blocks = own.filter((p) => p.type === 'list' || isMultiline(plugin.name, p.name));
  const inputs = own.filter((p) => !blocks.includes(p) && p.type !== 'bool');
  const hintOf = (spec: CheckPluginParam) =>
    spec.name === 'method' && changesState(plugin.name, values) ? t.checks.synthetic.methodChangesState : text(spec)?.hint;
  const switches = own.filter((p) => p.type === 'bool');
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
      {blocks.map((spec) => {
        const id = `${idPrefix}-${spec.name}`;
        const value = values[spec.name];
        const hint = text(spec)?.hint;
        if (isAssertions(plugin.name, spec.name) && isAssertionPlugin(plugin.name)) {
          return (
            <AssertionEditor
              // Keyed by plugin: rows and mode never carry over to another
              // plugin's subjects (backlog 3.87).
              key={`${plugin.name}.${spec.name}`}
              plugin={plugin.name}
              id={id}
              label={label(spec)}
              hint={hint}
              placeholder={placeholderOf(spec, text(spec))}
              value={typeof value === 'string' ? value : ''}
              error={errors[spec.name]}
              serverError={serverError}
              disabled={disabled}
              onChange={(next) => set(spec.name, next)}
            />
          );
        }
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
              <Field key={spec.name} id={id} label={label(spec)} hint={hintOf(spec)} error={errors[spec.name]}>
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
      {groups && (
        <VantageFields
          idPrefix={idPrefix}
          plugin={plugin}
          groups={groups}
          values={values}
          errors={errors}
          disabled={disabled}
          onChange={onChange}
          control={control}
        />
      )}
    </div>
  );
}

/** ServerKeyHint sits under a check's key field while the plugin runs on
 * the Pulsix server: alert rules apply to every check of the type,
 * whatever its key. */
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
