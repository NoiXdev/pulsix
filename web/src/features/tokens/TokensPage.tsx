import { useMemo, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm, useWatch } from 'react-hook-form';
import { z } from 'zod';
import { KeyRound } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import type { components } from '../../api/schema.d.ts';
import { useCreateToken, useRevokeToken, useTokens } from '../../api/hooks/useTokens.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { roleOptions } from '../members/roles.ts';
import type { Messages } from '../../i18n/types.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { Tag } from '../../ui/badge.tsx';
import { AreaGrid } from './AreaGrid.tsx';
import { EditTokenDialog } from './EditTokenDialog.tsx';
import { allowedAreas, displayedAreas, type Area } from './areas.ts';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
const KINDS = ['api', 'assistant'] as const;

const tokenFormSchema = (t: Messages) =>
  z
    .object({
      name: z.string().min(1, t.validation.required),
      role: z.enum(ROLES),
      kind: z.enum(KINDS),
      areas: z.array(z.custom<Area>()),
    })
    // An assistant token needs at least one area the role can cover; the
    // server refuses one without (422).
    .refine((v) => v.kind !== 'assistant' || allowedAreas(v.areas, v.role).length > 0, {
      path: ['areas'],
      message: t.tokens.areas.required,
    });
type TokenFormValues = z.infer<ReturnType<typeof tokenFormSchema>>;

const DEFAULTS: TokenFormValues = { name: '', role: 'viewer', kind: 'api', areas: [] };

function isExpired(expiresAt?: string): boolean {
  return !!expiresAt && new Date(expiresAt).getTime() <= Date.now();
}

const COLUMNS = 'minmax(0, 1fr) 100px 140px 140px 120px';

export function TokensPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const ask = useConfirm();
  const t = useT();
  const copy = useCopy();
  const fmt = useFormat();
  const schema = useMemo(() => tokenFormSchema(t), [t]);

  const tokens = useTokens(org);
  const createToken = useCreateToken(org);
  const revokeToken = useRevokeToken(org);

  const [createOpen, setCreateOpen] = useState(false);
  const [justCreated, setJustCreated] = useState<string | null>(null);
  const [editing, setEditing] = useState<components['schemas']['TokenDTO'] | null>(null);

  const form = useForm<TokenFormValues>({ resolver: zodResolver(schema), defaultValues: DEFAULTS });
  const kind = useWatch({ control: form.control, name: 'kind' });
  const role = useWatch({ control: form.control, name: 'role' });

  const tokensGuard = useQueryGuard(tokens);
  if (tokensGuard) return tokensGuard;

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.tokens.title}</h1>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <KeyRound size={15} /> {t.tokens.create}
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">
        {tokens.data ? t.tokens.count((tokens.data.tokens ?? []).length) : t.common.loading}
      </span>
      <p className="text-[13px] text-muted-foreground">
        {t.tokens.explorerHint}{' '}
        <a href="/api/explorer" target="_blank" rel="noopener noreferrer" className="text-foreground underline underline-offset-2">
          {t.tokens.explorerLink}
          <span className="sr-only"> {t.shell.opensInNewTab}</span>
        </a>
      </p>

      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.tokens.columns.name}</span>
          <span>{t.tokens.columns.role}</span>
          <span>{t.tokens.columns.lastUsed}</span>
          <span>{t.tokens.columns.expires}</span>
          <span />
        </GridHeader>
        {(tokens.data?.tokens ?? []).map((token) => (
          <GridRow key={token.id} columns={COLUMNS}>
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="flex min-w-0 items-center gap-2">
                <span className="truncate text-xs font-semibold">{token.name}</span>
                {token.kind === 'assistant' && <Tag>{t.tokens.kind.badge}</Tag>}
              </span>
              {token.kind === 'assistant' && (token.areas ?? []).length > 0 && (
                <span className="truncate text-[11px] text-muted-foreground">
                  {displayedAreas(token.areas ?? [])
                    .map((area) => (Object.hasOwn(t.tokens.areas.labels, area) ? t.tokens.areas.labels[area as Area] : area))
                    .join(' · ')}
                </span>
              )}
            </span>
            <RoleBadge role={token.role} />
            <span className="text-[11px] text-muted-foreground">
              {token.last_used_at ? fmt.date(token.last_used_at) : t.common.never}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {token.expires_at ? fmt.date(token.expires_at) : t.common.never}
            </span>
            <span className="flex items-center gap-3">
            <button
              type="button"
              className="text-[11px] disabled:opacity-50"
              disabled={isExpired(token.expires_at)}
              title={isExpired(token.expires_at) ? t.tokens.edit.expiredHint : undefined}
              aria-label={`${t.tokens.edit.action} ${token.name}`}
              onClick={() => setEditing(token)}
            >
              {t.tokens.edit.action}
            </button>
            <button
              type="button"
              className="text-[11px] text-destructive"
              onClick={async () => {
                if (!(await ask({ message: t.tokens.revokeConfirm(token.name), destructive: true }))) return;
                try {
                  await revokeToken.mutateAsync(token.id);
                  toast.show(t.tokens.revoked(token.name));
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : t.tokens.revokeFailed, 'destructive');
                }
              }}
            >
              {t.tokens.revoke}
            </button>
            </span>
          </GridRow>
        ))}
        {(tokens.data?.tokens ?? []).length === 0 && (
          <div className="p-4 text-xs text-muted-foreground">{t.tokens.empty}</div>
        )}
      </Card>

      {editing && <EditTokenDialog key={editing.id} org={org} token={editing} open onClose={() => setEditing(null)} />}

      <Dialog
        open={createOpen}
        onClose={() => {
          setCreateOpen(false);
          setJustCreated(null);
          createToken.reset();
          form.reset();
        }}
        title={t.tokens.create}
      >
        {justCreated ? (
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              {t.tokens.shownOnce}
            </p>
            <code className="break-all rounded-md border border-border bg-background p-3 font-mono text-xs">
              {justCreated}
            </code>
            <div className="flex justify-end gap-2">
              <Button
                onClick={() => void copy(justCreated)}
              >
                {t.tokens.copy}
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  setCreateOpen(false);
                  setJustCreated(null);
                  createToken.reset();
                  form.reset();
                }}
              >
                {t.tokens.done}
              </Button>
            </div>
          </div>
        ) : (
          <form
            className="flex flex-col gap-4"
            onSubmit={form.handleSubmit(async (values) => {
              try {
                const res = await createToken.mutateAsync(
                  values.kind === 'assistant'
                    ? { name: values.name, role: values.role, kind: 'assistant', areas: allowedAreas(values.areas, values.role) }
                    : { name: values.name, role: values.role, kind: 'api' },
                );
                setJustCreated(res.token);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.tokens.createFailed, 'destructive');
              }
            })}
          >
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="token_name">{t.tokens.nameLabel}</Label>
              <Input id="token_name" {...form.register('name')} placeholder="ci-deploy" />
              {form.formState.errors.name && (
                <span className="text-[11px] text-destructive">{form.formState.errors.name.message}</span>
              )}
            </div>
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 text-xs font-semibold">{t.tokens.kind.label}</legend>
              <div role="radiogroup" aria-label={t.tokens.kind.label} className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
                {KINDS.map((k) => (
                  <label key={k} className="flex items-center gap-2">
                    <input type="radio" value={k} {...form.register('kind')} />
                    {t.tokens.kind[k]}
                  </label>
                ))}
              </div>
              <span className="text-[11px] text-muted-foreground">
                {kind === 'assistant' ? t.tokens.kind.assistantHint : t.tokens.kind.apiHint}
              </span>
            </fieldset>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="token_role">{t.tokens.roleLabel}</Label>
              <Controller
                control={form.control}
                name="role"
                render={({ field }) => (
                  <Select
                    id="token_role"
                    options={roleOptions(t, ROLES)}
                    value={field.value}
                    onChange={(next) => {
                      field.onChange(next);
                      // A lower role drops the areas it cannot cover, so
                      // raising it again does not bring them back unseen.
                      form.setValue('areas', allowedAreas(form.getValues('areas'), next));
                    }}
                  />
                )}
              />
              <span className="text-[11px] text-muted-foreground">
                {t.tokens.roleHint}
              </span>
            </div>
            {kind === 'assistant' && (
              <Controller
                control={form.control}
                name="areas"
                render={({ field }) => (
                  <AreaGrid
                    role={role}
                    value={field.value}
                    onChange={field.onChange}
                    error={form.formState.errors.areas?.message}
                  />
                )}
              />
            )}
            <div className="flex justify-end gap-2">
              <Button type="button" onClick={() => setCreateOpen(false)}>
                {t.common.cancel}
              </Button>
              <Button type="submit" variant="primary" disabled={createToken.isPending}>
                {t.tokens.create}
              </Button>
            </div>
          </form>
        )}
      </Dialog>
    </>
  );
}
