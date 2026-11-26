import type { ReactNode } from 'react';
import { Field } from '../../ui/field.tsx';
import { Label } from '../../ui/label.tsx';
import { MultiSelect } from '../../ui/MultiSelect.tsx';
import { useT } from '../../i18n/useT.ts';
import type { CheckPluginDescriptor, CheckPluginParam, MonitoringGroup } from '../../api/types.ts';
import { FieldError } from '../instance/shared/formParts.tsx';
import {
  VANTAGE_AGENTS_PER_GROUP, VANTAGE_FAIL_POLICY, VANTAGE_GROUPS, VANTAGE_MANAGER, customRequest, vantageGroupsOf, paramText, type ParamValues,
} from './paramForm.ts';

/**
 * VantageFields edits where a synthetic check is probed from (backlog
 * 3.13a): the Pulsix server itself (a checkbox), the agents of the
 * organization's monitoring groups (picked by name, never typed as ids),
 * and — once a group is chosen — how many agents per group and when the
 * check as a whole is down. A check whose secret is set is probed only
 * from the server (the plugins' own rule), so the group picker is
 * disabled with that reason while a secret is named; so is an http check
 * with a custom request (backlog 3.84).
 *
 * The values stay in ParamFields' shape (the groups as one id per line),
 * so paramsFrom, paramValuesFrom and validateParams treat them like any
 * other list.
 */
export function VantageFields({
  idPrefix,
  plugin,
  groups,
  values,
  errors,
  disabled,
  onChange,
  control,
}: {
  idPrefix: string;
  plugin: CheckPluginDescriptor;
  /** The organization's monitoring groups. */
  groups: readonly MonitoringGroup[];
  values: ParamValues;
  errors: Record<string, string>;
  disabled?: boolean;
  onChange: (values: ParamValues) => void;
  /** ParamFields' own control for a parameter (the agents per group
   * number, the fail policy select). */
  control: (spec: CheckPluginParam, id: string) => ReactNode;
}) {
  const t = useT();
  const spec = (name: string) => plugin.params.find((p) => p.name === name);
  const text = (name: string) => paramText(t, plugin.name, name);
  const manager = spec(VANTAGE_MANAGER);
  const groupSpec = spec(VANTAGE_GROUPS);
  if (!manager || !groupSpec) return null;
  const extras = [spec(VANTAGE_AGENTS_PER_GROUP), spec(VANTAGE_FAIL_POLICY)].filter((s): s is CheckPluginParam => !!s);

  const chosen = vantageGroupsOf(values);
  const secret = typeof values.secret === 'string' && values.secret.trim() !== '';
  const custom = customRequest(plugin.name, values);
  const serverOnly = secret || custom;
  const managerId = `${idPrefix}-${VANTAGE_MANAGER}`;
  const groupsId = `${idPrefix}-${VANTAGE_GROUPS}`;
  const managerValue = values[VANTAGE_MANAGER];
  const groupsHint = secret
    ? t.checks.vantage.secretOnlyServer
    : custom
      ? t.checks.vantage.customRequestOnlyServer
      : text(VANTAGE_GROUPS)?.hint;

  return (
    <fieldset className="flex flex-col gap-3 rounded-md border border-border p-3">
      <legend className="px-1 text-xs font-semibold text-foreground">{t.checks.vantage.title}</legend>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2 text-xs">
          <input
            id={managerId}
            type="checkbox"
            disabled={disabled}
            checked={typeof managerValue === 'boolean' ? managerValue : manager.default !== false}
            aria-describedby={text(VANTAGE_MANAGER)?.hint ? `${managerId}-hint` : undefined}
            onChange={(e) => onChange({ ...values, [VANTAGE_MANAGER]: e.target.checked })}
          />
          <label htmlFor={managerId}>{text(VANTAGE_MANAGER)?.label ?? VANTAGE_MANAGER}</label>
        </div>
        {text(VANTAGE_MANAGER)?.hint && (
          <p id={`${managerId}-hint`} className="text-[11px] text-muted-foreground">
            {text(VANTAGE_MANAGER)?.hint}
          </p>
        )}
        <FieldError>{errors[VANTAGE_MANAGER]}</FieldError>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor={groupsId}>{text(VANTAGE_GROUPS)?.label ?? VANTAGE_GROUPS}</Label>
        <MultiSelect
          id={groupsId}
          listLabel={t.checks.vantage.chosenGroups}
          placeholder={t.checks.vantage.addGroup}
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
          value={chosen}
          // A secret or a custom http request closes the picker but leaves
          // the chosen groups removable, so a form that picked groups
          // before is not stuck.
          max={serverOnly ? 0 : groupSpec.max_entries}
          disabled={disabled}
          invalid={!!errors[VANTAGE_GROUPS]}
          aria-describedby={groupsHint ? `${groupsId}-hint` : undefined}
          onChange={(next) => onChange({ ...values, [VANTAGE_GROUPS]: next.join('\n') })}
        />
        {groupsHint && (
          <p id={`${groupsId}-hint`} className="text-[11px] text-muted-foreground">
            {groupsHint}
          </p>
        )}
        <FieldError>{errors[VANTAGE_GROUPS]}</FieldError>
      </div>
      {chosen.length > 0 && extras.length > 0 && (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {extras.map((s) => {
            const id = `${idPrefix}-${s.name}`;
            return (
              <Field key={s.name} id={id} label={text(s.name)?.label ?? s.name} hint={text(s.name)?.hint} error={errors[s.name]}>
                {control(s, id)}
              </Field>
            );
          })}
        </div>
      )}
    </fieldset>
  );
}
