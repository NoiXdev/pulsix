import { useState, type ReactNode } from 'react';
import { useParams } from '@tanstack/react-router';
import { FormPage } from '../../../ui/form-page.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { CreateGate, DefinitionLoader } from './DefinitionLoader.tsx';
import {
  useCheckDefinitions,
  useCreateCheckDefinition,
  useUpdateCheckDefinition,
} from '../../../api/hooks/useSharedCheckDefinitions.ts';
import { useCheckPlugins } from '../../../api/hooks/useCheckPlugins.ts';
import { useT } from '../../../i18n/useT.ts';
import type { Messages } from '../../../i18n/types.ts';
import type { CheckDefinitionDef, CheckDefinitionDefInput, CheckPluginDescriptor } from '../../../api/types.ts';
import { FIELD_GRID, FieldError, FormError, FormSection } from './formParts.tsx';
import { ParamFields, PluginsPending, ServerKeyHint } from '../../checks/ParamFields.tsx';
import {
  findPlugin, keyAfterPluginChange, paramsFrom, paramValuesFrom, runsOnServer, validateParams, type ParamValues,
} from '../../checks/paramForm.ts';

/**
 * SharedCheckFormPage creates (`…/checks/new`) or edits (`…/checks/$id`) one
 * installation-owned check definition. No binding, no enabled flag — decision
 * 2 keeps both on the projection, which the organization edits through its
 * own Checks page (CheckDialog.tsx), never here.
 *
 * The plugin list and every plugin's parameter fields come from the
 * descriptors (GET /check-plugins, backlog 3.54) — ParamFields, the same
 * fields CheckDialog.tsx shows; the form waits for them.
 */
export function SharedCheckFormPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id?: string };
  if (!id) {
    return (
      <CreateGate>
        <WithPlugins>{(plugins) => <CheckForm org={org} plugins={plugins} />}</WithPlugins>
      </CreateGate>
    );
  }
  return (
    <DefinitionLoader useDefinitions={useCheckDefinitions} id={id}>
      {(definition) => <WithPlugins>{(plugins) => <CheckForm org={org} definition={definition} plugins={plugins} />}</WithPlugins>}
    </DefinitionLoader>
  );
}

/** WithPlugins renders the form once the check plugins' descriptors have
 * arrived, and their loading or failure until then. */
function WithPlugins({ children }: { children: (plugins: CheckPluginDescriptor[]) => ReactNode }) {
  const plugins = useCheckPlugins();
  if (!plugins.data) return <PluginsPending failed={plugins.isError} error={plugins.error} />;
  return <>{children(plugins.data)}</>;
}

interface CheckDefinitionFormValues {
  key: string;
  plugin: string;
  intervalSeconds: string;
  /** The plugin's parameter fields (ParamFields). */
  params: ParamValues;
}

/** Mirrors checkDefinitionDefBody's own lack of format constraints
 * (internal/api/shared_handlers.go): checks.ValidateDefinitionShape is the
 * one validator every write funnels through, so this form only refuses what
 * would otherwise submit an empty, guaranteed-400 request. The parameters
 * are validateParams' (the descriptor's constraints). */
function validateCheckDefinitionInput(
  values: CheckDefinitionFormValues,
  t: Messages,
): Partial<Record<keyof CheckDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof CheckDefinitionFormValues, string>> = {};
  if (values.key.length > 63) errors.key = t.validation.maxChars(63);
  const interval = Number(values.intervalSeconds);
  if (!values.intervalSeconds || Number.isNaN(interval)) errors.intervalSeconds = t.validation.required;
  else if (interval < 10 || interval > 86400) errors.intervalSeconds = t.instance.shared.checks.intervalRange(10, 86400);
  return errors;
}

function CheckForm({ org, definition, plugins }: { org: string; definition?: CheckDefinitionDef; plugins: CheckPluginDescriptor[] }) {
  const t = useT();
  const toast = useToast();
  const create = useCreateCheckDefinition();
  const update = useUpdateCheckDefinition(definition?.id ?? '');
  const [initial] = useState<CheckDefinitionFormValues>(
    definition
      ? {
          key: definition.key, plugin: definition.plugin, intervalSeconds: String(definition.interval_seconds),
          params: paramValuesFrom(findPlugin(plugins, definition.plugin), definition.params),
        }
      : { key: '', plugin: 'cpu', intervalSeconds: '60', params: {} },
  );
  const [values, setValues] = useState<CheckDefinitionFormValues>(initial);
  const [errors, setErrors] = useState<Partial<Record<keyof CheckDefinitionFormValues, string>>>({});
  const [paramErrors, setParamErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function choosePlugin(next: string) {
    setParamErrors({});
    setValues((cur) => ({
      ...cur,
      plugin: next,
      // A new definition's key follows a server-run plugin (the key alert
      // rules match its metrics on); an existing one's key is never
      // renamed behind the user's back.
      key: definition ? cur.key : keyAfterPluginChange(cur.key, cur.plugin, next, plugins),
      // Another plugin's values would mostly be refused by this one; going
      // back to the stored plugin restores what is saved.
      params: definition && next === definition.plugin ? paramValuesFrom(findPlugin(plugins, next), definition.params) : {},
    }));
  }

  const descriptor = findPlugin(plugins, values.plugin);
  const onServer = runsOnServer(plugins, values.plugin);

  function params(): Record<string, unknown> {
    // Only the fields the form holds a value for, an empty optional list
    // as {} (every mount, interface, sensor, …).
    if (descriptor) return paramsFrom(descriptor, values.params);
    // A stored plugin the descriptors do not list has no fields to edit:
    // what is saved goes back unchanged rather than being dropped.
    if (definition && definition.plugin === values.plugin) return definition.params ?? {};
    return {};
  }

  async function submit(): Promise<boolean> {
    setFormError(null);
    const fieldErrors = validateCheckDefinitionInput(values, t);
    const refused = descriptor ? validateParams(descriptor, values.params, t) : {};
    setErrors(fieldErrors);
    setParamErrors(refused);
    if (Object.keys(fieldErrors).length > 0 || Object.keys(refused).length > 0) return false;
    const body: CheckDefinitionDefInput = {
      ...(values.key !== '' ? { key: values.key } : {}),
      plugin: values.plugin, interval_seconds: Number(values.intervalSeconds), params: params(),
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.checks.saved(body.plugin) : t.instance.shared.checks.created(body.plugin),
      );
      return true;
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
      return false;
    }
  }

  return (
    <FormPage
      title={definition ? definition.key : t.instance.shared.checks.newTitle}
      backTo={{ to: '/o/$org/settings/shared/checks', params: { org } }}
      onSubmit={submit}
      submitting={create.isPending || update.isPending}
      dirty={JSON.stringify(values) !== JSON.stringify(initial)}
      submitLabel={definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
    >
      <FormSection hint={t.instance.shared.checks.hint}>
        <div className={FIELD_GRID}>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-plugin">{t.instance.shared.checks.plugin}</Label>
            <Select
              id="check-def-plugin"
              options={plugins.map((p) => ({
                value: p.name,
                label: t.instance.shared.checks.plugins[p.name],
                ...(p.runs === 'server' ? { hint: t.checks.synthetic.pluginHint } : {}),
              }))}
              value={values.plugin}
              onChange={choosePlugin}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-key">{t.instance.shared.checks.key}</Label>
            <Input
              id="check-def-key"
              className="font-mono"
              value={values.key}
              placeholder={values.plugin}
              onChange={(e) => setValues((cur) => ({ ...cur, key: e.target.value.trim() }))}
              aria-invalid={errors.key ? true : undefined}
              aria-describedby={onServer ? 'check-def-key-hint' : undefined}
            />
            <FieldError>{errors.key}</FieldError>
            {onServer && <ServerKeyHint id="check-def-key-hint" plugin={values.plugin} />}
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-interval">{t.instance.shared.checks.intervalSeconds}</Label>
            <Input
              id="check-def-interval"
              type="number"
              min={10}
              max={86400}
              value={values.intervalSeconds}
              onChange={(e) => setValues((cur) => ({ ...cur, intervalSeconds: e.target.value }))}
              aria-invalid={errors.intervalSeconds ? true : undefined}
            />
            <FieldError>{errors.intervalSeconds}</FieldError>
          </div>
        </div>
        {descriptor && (
          <ParamFields
            idPrefix="check-def-param"
            plugin={descriptor}
            values={values.params}
            errors={paramErrors}
            onChange={(params) => setValues((cur) => ({ ...cur, params }))}
          />
        )}
      </FormSection>
      <FormError>{formError}</FormError>
    </FormPage>
  );
}
