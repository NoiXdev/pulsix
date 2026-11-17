import { useMemo, useState } from 'react';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm, useWatch } from 'react-hook-form';
import { z } from 'zod';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useUpdateToken, type UpdateTokenInput } from '../../api/hooks/useTokens.ts';
import type { components } from '../../api/schema.d.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';
import { AreaGrid } from './AreaGrid.tsx';
import { allowedAreas, type Area } from './areas.ts';

type TokenRow = components['schemas']['TokenDTO'];

const editSchema = (t: Messages) =>
  z.object({
    name: z.string().min(1, t.validation.required),
    expiry: z.string(),
    never: z.boolean(),
    areas: z.array(z.custom<Area>()),
  });
type EditValues = z.infer<ReturnType<typeof editSchema>>;

/** The local calendar day of an instant as YYYY-MM-DD, for a date input. */
function localDay(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The last millisecond of a YYYY-MM-DD day in local time, as an ISO instant. */
function endOfLocalDay(day: string): string {
  const [y, m, d] = day.split('-').map(Number);
  return new Date(y, m - 1, d, 23, 59, 59, 999).toISOString();
}

function sameAreas(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && new Set(a).size === new Set([...a, ...b]).size;
}

/**
 * EditTokenDialog changes a token's name, expiry and (assistant tokens) areas.
 * Kind and role are fixed. Only the keys that differ from the token are sent,
 * so an untouched form sends `{}` and the server's "nothing to change" answer
 * is shown as it is.
 */
export function EditTokenDialog({
  org,
  token,
  open,
  onClose,
}: {
  org: string;
  token: TokenRow;
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const schema = useMemo(() => editSchema(t), [t]);
  const toast = useToast();
  const update = useUpdateToken(org);
  const [error, setError] = useState<string | null>(null);

  const isAssistant = token.kind === 'assistant';
  const initialDay = token.expires_at ? localDay(token.expires_at) : '';
  const initialAreas = allowedAreas((token.areas ?? []) as Area[], token.role);

  const form = useForm<EditValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: token.name, expiry: initialDay, never: !token.expires_at, areas: initialAreas },
  });
  const never = useWatch({ control: form.control, name: 'never' });

  const submit = form.handleSubmit(async (values) => {
    setError(null);
    const body: UpdateTokenInput = {};
    if (values.name !== token.name) body.name = values.name;
    const day = values.never ? '' : values.expiry;
    if (day !== initialDay) {
      if (day === '') body.never_expires = true;
      else body.expires_at = endOfLocalDay(day);
    }
    const areas = allowedAreas(values.areas, token.role);
    const areasChanged = isAssistant && !sameAreas(areas, initialAreas);
    if (areasChanged) body.areas = areas;
    try {
      await update.mutateAsync({ id: token.id, body });
      toast.show(areasChanged ? t.tokens.edit.savedReconnect(values.name) : t.tokens.edit.saved(values.name));
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.tokens.edit.failed);
    }
  });

  return (
    <Dialog open={open} onClose={onClose} title={t.tokens.edit.title(token.name)}>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="edit_token_name">{t.tokens.nameLabel}</Label>
          <Input id="edit_token_name" {...form.register('name')} />
          {form.formState.errors.name && (
            <span className="text-[11px] text-destructive">{form.formState.errors.name.message}</span>
          )}
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-xs">
            {t.tokens.kind.label}: {t.tokens.kind[isAssistant ? 'assistant' : 'api']} · {t.tokens.roleLabel}:{' '}
            {roleLabel(t, token.role)}
          </span>
          <span className="text-[11px] text-muted-foreground">{t.tokens.edit.kindRoleFixed}</span>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="edit_token_expiry">{t.tokens.edit.expiresLabel}</Label>
          <Input
            id="edit_token_expiry"
            type="date"
            disabled={never}
            {...form.register('expiry', {
              onChange: (e) => {
                if (e.target.value) form.setValue('never', false);
              },
            })}
          />
          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              {...form.register('never', {
                onChange: (e) => {
                  if (e.target.checked) form.setValue('expiry', '');
                },
              })}
            />
            {t.tokens.edit.neverExpires}
          </label>
        </div>
        {isAssistant && (
          <Controller
            control={form.control}
            name="areas"
            render={({ field }) => <AreaGrid role={token.role} value={field.value} onChange={field.onChange} />}
          />
        )}
        {error && (
          <span role="alert" className="text-[11px] text-destructive">
            {error}
          </span>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={update.isPending}>
            {t.tokens.edit.save}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
