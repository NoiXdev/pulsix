import { useState } from 'react';
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
import { useT } from '../../../i18n/useT.ts';
import type { Messages } from '../../../i18n/types.ts';
import type { CheckDefinitionDef, CheckDefinitionDefInput } from '../../../api/types.ts';
import { FIELD_GRID, FieldError, FormError, FormSection } from './formParts.tsx';

/**
 * SharedCheckFormPage creates (`…/checks/new`) or edits (`…/checks/$id`) one
 * installation-owned check definition. No binding, no enabled flag — decision
 * 2 keeps both on the projection, which the organization edits through its
 * own Checks page (CheckDialog.tsx), never here.
 *
 * Params editing covers the "service" plugin's units and the "disk" plugin's
 * mounts, the parameters an instance admin is likely to set on a shared
 * check, rather than re-hosting CheckDialog.tsx's full per-plugin editor for
 * a form that has no per-organization state to show.
 */
export function SharedCheckFormPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id?: string };
  if (!id) {
    return (
      <CreateGate>
        <CheckForm org={org} />
      </CreateGate>
    );
  }
  return (
    <DefinitionLoader useDefinitions={useCheckDefinitions} id={id}>
      {(definition) => <CheckForm org={org} definition={definition} />}
    </DefinitionLoader>
  );
}

interface CheckDefinitionFormValues {
  key: string;
  plugin: string;
  intervalSeconds: string;
  units: string;
  mounts: string;
  interfaces: string;
}

const CHECK_PLUGINS = ['cpu', 'memory', 'disk', 'service', 'network', 'uptime'] as const;

/** lines turns a textarea's content into the string list the API takes —
 * CheckDialog.tsx's own helper, duplicated rather than imported: that
 * module's own export surface is the dialog component, not this one helper,
 * and the function is three lines. */
function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** Mirrors checkDefinitionDefBody's own lack of format constraints
 * (internal/api/shared_handlers.go): checks.ValidateDefinitionShape is the
 * one validator every write funnels through, so this form only refuses what
 * would otherwise submit an empty, guaranteed-400 request. */
function validateCheckDefinitionInput(
  values: CheckDefinitionFormValues,
  t: Messages,
): Partial<Record<keyof CheckDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof CheckDefinitionFormValues, string>> = {};
  if (values.key.length > 63) errors.key = t.validation.maxChars(63);
  const interval = Number(values.intervalSeconds);
  if (!values.intervalSeconds || Number.isNaN(interval)) errors.intervalSeconds = t.validation.required;
  else if (interval < 10 || interval > 86400) errors.intervalSeconds = t.instance.shared.checks.intervalRange(10, 86400);
  if (values.plugin === 'service' && lines(values.units).length === 0) errors.units = t.validation.required;
  return errors;
}

function CheckForm({ org, definition }: { org: string; definition?: CheckDefinitionDef }) {
  const t = useT();
  const toast = useToast();
  const create = useCreateCheckDefinition();
  const update = useUpdateCheckDefinition(definition?.id ?? '');
  const [initial] = useState<CheckDefinitionFormValues>(
    definition
      ? {
          key: definition.key, plugin: definition.plugin, intervalSeconds: String(definition.interval_seconds),
          units: Array.isArray(definition.params?.units) ? (definition.params.units as string[]).join('\n') : '',
          mounts: Array.isArray(definition.params?.mounts) ? (definition.params.mounts as string[]).join('\n') : '',
          interfaces: Array.isArray(definition.params?.interfaces) ? (definition.params.interfaces as string[]).join('\n') : '',
        }
      : { key: '', plugin: 'cpu', intervalSeconds: '60', units: '', mounts: '', interfaces: '' },
  );
  const [values, setValues] = useState<CheckDefinitionFormValues>(initial);
  const [errors, setErrors] = useState<Partial<Record<keyof CheckDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function params(): Record<string, unknown> {
    if (values.plugin === 'service') return { units: lines(values.units) };
    // disk's allow-list is optional and an empty one means every mount
    // (CheckDialog.tsx keeps the same contract for an organization's own
    // checks), so an empty textarea sends {} rather than an empty list.
    if (values.plugin === 'disk') {
      const list = lines(values.mounts);
      return list.length > 0 ? { mounts: list } : {};
    }
    // network's interface list is optional the same way: empty means every
    // real interface, so an empty textarea sends {}.
    if (values.plugin === 'network') {
      const list = lines(values.interfaces);
      return list.length > 0 ? { interfaces: list } : {};
    }
    // cpu, memory and uptime take no params at all. Any plugin added later is
    // still unrendered here, so its stored value round-trips unchanged
    // while the plugin itself is unchanged — switching plugins away from
    // what params() was written against would carry over a shape the new
    // plugin's own validation refuses anyway, so {} is right there.
    if (definition && definition.plugin === values.plugin) return definition.params ?? {};
    return {};
  }

  async function submit(): Promise<boolean> {
    setFormError(null);
    const fieldErrors = validateCheckDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return false;
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
              options={CHECK_PLUGINS.map((value) => ({ value, label: t.instance.shared.checks.plugins[value] }))}
              value={values.plugin}
              onChange={(v) => setValues((cur) => ({ ...cur, plugin: v }))}
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
            />
            <FieldError>{errors.key}</FieldError>
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
        {values.plugin === 'disk' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-mounts">{t.instance.shared.checks.mounts}</Label>
            <textarea
              id="check-def-mounts"
              className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
              value={values.mounts}
              placeholder="/&#10;/var"
              onChange={(e) => setValues((cur) => ({ ...cur, mounts: e.target.value }))}
            />
          </div>
        )}
        {values.plugin === 'network' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-interfaces">{t.instance.shared.checks.interfaces}</Label>
            <textarea
              id="check-def-interfaces"
              className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
              value={values.interfaces}
              placeholder="eth0&#10;bond0"
              aria-describedby="check-def-interfaces-hint"
              onChange={(e) => setValues((cur) => ({ ...cur, interfaces: e.target.value }))}
            />
            <p id="check-def-interfaces-hint" className="text-[11px] text-muted-foreground">{t.instance.shared.checks.interfacesHint}</p>
          </div>
        )}
        {values.plugin === 'service' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-def-units">{t.instance.shared.checks.units}</Label>
            <textarea
              id="check-def-units"
              className="min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs"
              value={values.units}
              placeholder="sshd"
              onChange={(e) => setValues((cur) => ({ ...cur, units: e.target.value }))}
            />
            <FieldError>{errors.units}</FieldError>
          </div>
        )}
      </FormSection>
      <FormError>{formError}</FormError>
    </FormPage>
  );
}
