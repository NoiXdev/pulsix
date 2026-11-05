import { useState, type ChangeEvent } from 'react';
import { useParams } from '@tanstack/react-router';
import { FormPage } from '../../../ui/form-page.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { CreateGate, DefinitionLoader } from './DefinitionLoader.tsx';
import { SchemaBuilder } from '../../variables/SchemaBuilder.tsx';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { toVarSchema } from '../../../api/hooks/useInventory.ts';
import type { FieldType, VarSchema, Vars } from '../../variables/types.ts';
import { useRepoDefinitions } from '../../../api/hooks/useSharedRepoDefinitions.ts';
import {
  useCreateTemplateDefinition,
  useTemplateDefinitions,
  useUpdateTemplateDefinition,
} from '../../../api/hooks/useSharedTemplateDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { Messages } from '../../../i18n/types.ts';
import type { TemplateDefinition, TemplateDefinitionInput } from '../../../api/types.ts';
import { FIELD_GRID, FieldError, FormError, FormSection } from './formParts.tsx';

/**
 * SharedTemplateFormPage creates (`…/templates/new`) or edits
 * (`…/templates/$id`) one installation-owned job template definition. No
 * scope, no SSH credential, no vault identities — decision 2 keeps every
 * binding inside the organization that supplies it, never on the definition.
 *
 * The shared repositories are this form's one dependency on the other list:
 * a template always resolves its playbook against a shared repository, never
 * an organization's own (shared_job_templates.shared_repo_id's own NOT NULL,
 * migration 00061). An empty repository list is a legitimate state (none
 * exists yet), not an error; validation refuses to submit without one. A
 * repository list that fails to load is an error, shown as such
 * (TemplateFormWithRepos), never an empty picker.
 */
export function SharedTemplateFormPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id?: string };
  if (!id) {
    return (
      <CreateGate>
        <TemplateFormWithRepos org={org} />
      </CreateGate>
    );
  }
  return (
    <DefinitionLoader useDefinitions={useTemplateDefinitions} id={id}>
      {(definition) => <TemplateFormWithRepos org={org} definition={definition} />}
    </DefinitionLoader>
  );
}

/** The repository picker's options come from the repositories list, which
 * has to be there before the form is: a picker that is silently empty
 * because that query failed would read as "no repository exists yet", and
 * the form could not be submitted. So the list loads (or fails, with its
 * reason) first. */
function TemplateFormWithRepos({ org, definition }: { org: string; definition?: TemplateDefinition }) {
  const repoDefinitions = useRepoDefinitions();
  const guard = useQueryGuard(repoDefinitions);
  if (guard) return guard;
  if (!repoDefinitions.data) return <QueryLoading />;
  const repoOptions = (repoDefinitions.data.definitions ?? []).map((d) => ({ value: d.id, label: d.name }));
  return <TemplateForm org={org} definition={definition} repoOptions={repoOptions} />;
}

interface TemplateDefinitionFormValues {
  name: string;
  sharedRepoId: string;
  playbookPath: string;
  limit: string;
}

/** Mirrors templateDefinitionBody's own minLength/maxLength exactly
 * (internal/api/shared_handlers.go), no format pattern beyond that — the
 * repository form's validateDefinitionInput has the reasoning. */
function validateTemplateDefinitionInput(
  values: TemplateDefinitionFormValues,
  t: Messages,
): Partial<Record<keyof TemplateDefinitionFormValues, string>> {
  const errors: Partial<Record<keyof TemplateDefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = t.validation.required;
  else if (values.name.length > 63) errors.name = t.validation.maxChars(63);
  if (values.sharedRepoId.trim() === '') errors.sharedRepoId = t.validation.required;
  if (values.playbookPath.trim() === '') errors.playbookPath = t.validation.required;
  else if (values.playbookPath.length > 500) errors.playbookPath = t.validation.maxChars(500);
  if (values.limit.length > 500) errors.limit = t.validation.maxChars(500);
  return errors;
}

// SHARED_FIELD_TYPES is FIELD_TYPES without "secret": a shared
// definition's extra vars are written straight into every organization's
// own job_templates.extra_vars column, never sealed, so the server
// refuses a secret-typed field outright (internal/shared's
// schemaHasSecretField). Offering a type that is always refused is worse
// than not offering it (backlog 2.101).
const SHARED_FIELD_TYPES: readonly FieldType[] = ['string', 'int', 'float', 'bool', 'enum', 'list', 'repeater', 'object'];

type TextKey = Exclude<keyof TemplateDefinitionFormValues, 'sharedRepoId'>;

function TemplateForm({
  org, definition, repoOptions,
}: {
  org: string;
  definition?: TemplateDefinition;
  repoOptions: { value: string; label: string }[];
}) {
  const t = useT();
  const toast = useToast();
  const labels: Record<TextKey, string> = {
    name: t.instance.shared.name,
    playbookPath: t.instance.shared.templates.playbookPath,
    limit: t.instance.shared.templates.limitPattern,
  };
  const create = useCreateTemplateDefinition();
  const update = useUpdateTemplateDefinition(definition?.id ?? '');
  const [initial] = useState(() => ({
    values: definition
      ? { name: definition.name, sharedRepoId: definition.shared_repo_id, playbookPath: definition.playbook_path, limit: definition.limit }
      : { name: '', sharedRepoId: '', playbookPath: '', limit: '' },
    schema: (definition?.extra_vars_schema ? toVarSchema(definition.extra_vars_schema) : { fields: [] }) as VarSchema,
    vars: (definition?.extra_vars ?? {}) as Vars,
  }));
  const [values, setValues] = useState<TemplateDefinitionFormValues>(initial.values);
  const [errors, setErrors] = useState<Partial<Record<keyof TemplateDefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [schema, setSchema] = useState<VarSchema>(initial.schema);
  const [vars, setVars] = useState<Vars>(initial.vars);

  function setValue(key: TextKey) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: TextKey, placeholder: string) {
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`tpl-def-${key}`}>{labels[key]}</Label>
        <Input
          id={`tpl-def-${key}`}
          className="font-mono"
          value={values[key]}
          onChange={setValue(key)}
          placeholder={placeholder}
          aria-invalid={errors[key] ? true : undefined}
          autoFocus={key === 'name'}
        />
        <FieldError>{errors[key]}</FieldError>
      </div>
    );
  }

  async function submit(): Promise<boolean> {
    setFormError(null);
    const fieldErrors = validateTemplateDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return false;
    // A PUT replaces everything, so the extra vars and their schema go
    // through the form's own state — an edit that does not touch them leaves
    // them as they were. An empty field list sends no schema at all rather
    // than an empty one, so a definition that never had one still round-trips
    // as absent.
    const body: TemplateDefinitionInput = {
      name: values.name, shared_repo_id: values.sharedRepoId, playbook_path: values.playbookPath, limit: values.limit,
      extra_vars: vars,
      ...(schema.fields.length > 0 ? { extra_vars_schema: schema } : {}),
    };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.templates.saved(body.name) : t.instance.shared.templates.created(body.name),
      );
      return true;
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
      return false;
    }
  }

  const dirty =
    JSON.stringify([values, schema, vars]) !== JSON.stringify([initial.values, initial.schema, initial.vars]);

  return (
    <FormPage
      title={definition?.name ?? t.instance.shared.templates.newTitle}
      backTo={{ to: '/o/$org/settings/shared/templates', params: { org } }}
      onSubmit={submit}
      submitting={create.isPending || update.isPending}
      dirty={dirty}
      submitLabel={definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
    >
      <FormSection hint={t.instance.shared.templates.hint}>
        <div className={FIELD_GRID}>
          {field('name', 'deploy-site')}
          <div className="flex flex-col gap-1">
            <Label htmlFor="tpl-def-shared-repo">{t.instance.shared.templates.sharedRepository}</Label>
            <Select
              id="tpl-def-shared-repo"
              invalid={!!errors.sharedRepoId}
              options={repoOptions}
              value={values.sharedRepoId}
              onChange={(v) => setValues((cur) => ({ ...cur, sharedRepoId: v }))}
              placeholder={t.instance.shared.templates.sharedRepositoryPlaceholder}
            />
            <FieldError>{errors.sharedRepoId}</FieldError>
          </div>
          {field('playbookPath', 'site.yml')}
          {field('limit', t.instance.shared.templates.limitPlaceholder)}
        </div>
      </FormSection>
      {/* The extra vars editor is SchemaBuilder (and SchemaForm for the
          values), kept as it is until backlog 7.75 replaces it; it lives in
          this one bounded section so that swap touches nothing else. No
          secret type: the server refuses one on a shared definition by
          construction, because these extra vars are written into every
          organization's own column without ever being sealed. */}
      <FormSection
        title={t.instance.shared.templates.extraVarsSection}
        titleId="tpl-def-extra-vars"
        hint={t.instance.shared.templates.extraVarsSectionHint}
      >
        <div className="flex flex-col gap-2">
          <span className="text-xs font-semibold">{t.instance.shared.templates.extraVarsSchema}</span>
          <SchemaBuilder nested types={SHARED_FIELD_TYPES} value={schema} onChange={setSchema} />
        </div>
        {schema.fields.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-semibold">{t.instance.shared.templates.extraVars}</span>
            <SchemaForm fields={schema.fields} value={vars} onChange={setVars} idPrefix="tpl-def-vars" />
          </div>
        )}
      </FormSection>
      <FormError>{formError}</FormError>
    </FormPage>
  );
}
