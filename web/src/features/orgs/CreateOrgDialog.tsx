import { useMemo } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Field } from '../../ui/field.tsx';
import { Input } from '../../ui/input.tsx';
import { SlugField } from '../../ui/SlugField.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { ApiError } from '../../api/client.ts';
import { useCreateOrg } from '../../api/hooks/useOrgs.ts';
import { schemas, type OrgFormValues } from '../../lib/schemas.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * CreateOrgDialog creates an organization (POST /orgs, instance admins
 * only): a name and a slug derived from it. On success it closes and takes
 * the creator, who is the new organization's owner, to its hosts page.
 * A slug that is taken (409) is shown at the slug field; any other failure
 * is a toast.
 *
 * It is rendered by whoever offers the entry: the org switcher in the two
 * layouts and the Organizations page.
 */
export function CreateOrgDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const create = useCreateOrg();
  const o = t.instance.orgs.create;
  const { orgFormSchema } = useMemo(() => schemas(t), [t]);
  const form = useForm<OrgFormValues>({
    resolver: zodResolver(orgFormSchema),
    defaultValues: { slug: '', name: '' },
  });
  const errors = form.formState.errors;

  function close() {
    form.reset();
    onClose();
  }

  return (
    <Dialog open={open} onClose={close} title={o.title}>
      <form
        className="flex flex-col gap-4"
        onSubmit={form.handleSubmit(async (values) => {
          try {
            const res = await create.mutateAsync(values);
            toast.show(o.created(res.org.name));
            close();
            await navigate({ to: '/o/$org/hosts', params: { org: res.org.slug }, search: { q: undefined } });
          } catch (err) {
            if (err instanceof ApiError && err.status === 409) {
              form.setError('slug', { message: o.slugTaken });
            } else {
              toast.show(err instanceof Error && err.message ? err.message : o.failed, 'destructive');
            }
          }
        })}
      >
        <Field id="org_name" label={o.name} error={errors.name?.message}>
          <Input id="org_name" autoComplete="off" {...form.register('name')} />
        </Field>
        <Field id="org_slug" label={o.slug} hint={o.slugHint} error={errors.slug?.message}>
          <SlugField
            id="org_slug"
            inputProps={form.register('slug')}
            readSource={() => form.getValues('name') ?? ''}
            write={(slug) => form.setValue('slug', slug, { shouldValidate: true })}
          />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={close}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            {o.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
