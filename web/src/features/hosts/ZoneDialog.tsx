import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { SlugField } from '../../ui/SlugField.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateZone } from '../../api/hooks/useHosts.ts';
import { zoneFormSchema, type ZoneFormValues } from '../../lib/schemas.ts';

/**
 * ZoneDialog creates a zone: name and a derived slug. Lifted out of
 * HostsListPage (v0.4g Task 7) so the Zone picker in the Add host form can
 * open it too, alongside the page's own "Add zone" button — the two
 * entry points share this one component rather than each growing their
 * own copy of the form.
 */
export function ZoneDialog({
  org, open, onClose, onCreated, initialName,
}: {
  org: string;
  open: boolean;
  onClose: () => void;
  /** Called with the new zone's id, so a picker that opened this can select it. */
  onCreated: (id: string) => void;
  /** Prefills the name, from what had been typed into the picker. Same prop shape AddCredentialDialog and BastionDialog give. */
  initialName?: string;
}) {
  const toast = useToast();
  const createZone = useCreateZone(org);
  const zoneForm = useForm<ZoneFormValues>({
    resolver: zodResolver(zoneFormSchema),
    defaultValues: { slug: '', name: initialName ?? '' },
  });

  return (
    <Dialog open={open} onClose={onClose} title="Add zone">
      <form
        className="flex flex-col gap-4"
        onSubmit={zoneForm.handleSubmit(async (values) => {
          try {
            const res = await createZone.mutateAsync(values);
            toast.show(`Zone ${values.name} created`);
            onCreated(res.zone.id);
            onClose();
          } catch (err) {
            toast.show(err instanceof Error ? err.message : 'failed to create zone', 'destructive');
          }
        })}
      >
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="zone_name">Name</Label>
          <Input id="zone_name" {...zoneForm.register('name')} />
          {zoneForm.formState.errors.name && (
            <span className="text-[11px] text-destructive">{zoneForm.formState.errors.name.message}</span>
          )}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="zone_slug">Slug</Label>
          <SlugField
            id="zone_slug"
            inputProps={zoneForm.register('slug')}
            readSource={() => zoneForm.getValues('name') ?? ''}
            write={(slug) => zoneForm.setValue('slug', slug, { shouldValidate: true })}
          />
          {zoneForm.formState.errors.slug && (
            <span className="text-[11px] text-destructive">{zoneForm.formState.errors.slug.message}</span>
          )}
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={createZone.isPending}>
            Create zone
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
