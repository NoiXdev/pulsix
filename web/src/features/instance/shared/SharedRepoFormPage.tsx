import { useState, type ChangeEvent } from 'react';
import { useParams } from '@tanstack/react-router';
import { FormPage } from '../../../ui/form-page.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { CreateGate, DefinitionLoader } from './DefinitionLoader.tsx';
import {
  useCreateRepoDefinition,
  useRepoDefinitions,
  useUpdateRepoDefinition,
} from '../../../api/hooks/useSharedRepoDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { Messages } from '../../../i18n/types.ts';
import type { RepoDefinition, RepoDefinitionInput } from '../../../api/types.ts';
import { FIELD_GRID, FieldError, FormError, FormSection } from './formParts.tsx';

/**
 * SharedRepoFormPage creates (`…/repos/new`) or edits (`…/repos/$id`) one
 * installation-owned repository definition. No credential field — decision 3
 * keeps every credential inside the organization that supplies it, never on
 * the definition a worker in any organization's zone could reach.
 */
export function SharedRepoFormPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id?: string };
  if (!id) {
    return (
      <CreateGate>
        <RepoForm org={org} />
      </CreateGate>
    );
  }
  return (
    <DefinitionLoader useDefinitions={useRepoDefinitions} id={id}>
      {(definition) => <RepoForm org={org} definition={definition} />}
    </DefinitionLoader>
  );
}

interface DefinitionFormValues {
  name: string;
  url: string;
  branch: string;
  subpath: string;
}

/**
 * validateDefinitionInput mirrors repoDefinitionBody's own bounds
 * (internal/api/shared_handlers.go: minLength/maxLength only, no pattern) —
 * unlike features/repos/validate.ts, which mirrors internal/repos/
 * service.go's own regex checks on an organization's repository.
 * shared.RepoDefinitionInput deliberately enforces no format beyond huma's
 * own schema layer (see shared_handlers.go's own comment on
 * repoDefinitionBody), so reusing repos/validate.ts's stricter patterns
 * here would reject input the server would accept — this stays only as
 * strict as the server actually is.
 */
function validateDefinitionInput(
  values: DefinitionFormValues,
  t: Messages,
): Partial<Record<keyof DefinitionFormValues, string>> {
  const errors: Partial<Record<keyof DefinitionFormValues, string>> = {};
  if (values.name.trim() === '') errors.name = t.validation.required;
  else if (values.name.length > 63) errors.name = t.validation.maxChars(63);
  if (values.url.trim() === '') errors.url = t.validation.required;
  else if (values.url.length > 500) errors.url = t.validation.maxChars(500);
  if (values.branch.length > 100) errors.branch = t.validation.maxChars(100);
  if (values.subpath.length > 200) errors.subpath = t.validation.maxChars(200);
  return errors;
}

function RepoForm({ org, definition }: { org: string; definition?: RepoDefinition }) {
  const t = useT();
  const toast = useToast();
  const labels: Record<keyof DefinitionFormValues, string> = {
    name: t.instance.shared.name,
    url: t.instance.shared.repos.url,
    branch: t.instance.shared.repos.branch,
    subpath: t.instance.shared.repos.subdirectory,
  };
  const create = useCreateRepoDefinition();
  const update = useUpdateRepoDefinition(definition?.id ?? '');
  const [initial] = useState<DefinitionFormValues>(
    definition
      ? { name: definition.name, url: definition.url, branch: definition.branch, subpath: definition.subpath }
      : { name: '', url: '', branch: '', subpath: '' },
  );
  const [values, setValues] = useState<DefinitionFormValues>(initial);
  const [errors, setErrors] = useState<Partial<Record<keyof DefinitionFormValues, string>>>({});
  const [formError, setFormError] = useState<string | null>(null);

  function setValue(key: keyof DefinitionFormValues) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: keyof DefinitionFormValues, placeholder: string, className?: string) {
    return (
      <div className={`flex flex-col gap-1 ${className ?? ''}`}>
        <Label htmlFor={`repo-def-${key}`}>{labels[key]}</Label>
        <Input
          id={`repo-def-${key}`}
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
    const fieldErrors = validateDefinitionInput(values, t);
    setErrors(fieldErrors);
    if (Object.keys(fieldErrors).length > 0) return false;
    const body: RepoDefinitionInput = { name: values.name, url: values.url, branch: values.branch, subpath: values.subpath };
    try {
      if (definition) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(
        definition ? t.instance.shared.repos.saved(body.name) : t.instance.shared.repos.created(body.name),
      );
      return true;
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t.instance.shared.saveFailed);
      return false;
    }
  }

  return (
    <FormPage
      title={definition?.name ?? t.instance.shared.repos.newTitle}
      backTo={{ to: '/o/$org/settings/shared/repos', params: { org } }}
      onSubmit={submit}
      submitting={create.isPending || update.isPending}
      dirty={JSON.stringify(values) !== JSON.stringify(initial)}
      submitLabel={definition ? t.instance.shared.saveDefinition : t.instance.shared.createDefinition}
    >
      <FormSection hint={t.instance.shared.repos.hint}>
        <div className={FIELD_GRID}>
          {field('name', 'ansible-base')}
          {field('branch', 'main')}
          {field('url', 'https://git.example/provider/ansible-base.git', 'md:col-span-2')}
          {field('subpath', t.instance.shared.repos.subpathPlaceholder, 'md:col-span-2')}
        </div>
      </FormSection>
      <FormError>{formError}</FormError>
    </FormPage>
  );
}
