import { useMemo, useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm } from 'react-hook-form';
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
import { useCreateToken, useRevokeToken, useTokens } from '../../api/hooks/useTokens.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { roleOptions } from '../members/roles.ts';
import type { Messages } from '../../i18n/types.ts';

const ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;

const tokenFormSchema = (t: Messages) =>
  z.object({
    name: z.string().min(1, t.validation.required),
    role: z.enum(ROLES),
  });
type TokenFormValues = z.infer<ReturnType<typeof tokenFormSchema>>;

const COLUMNS = 'minmax(0, 1fr) 100px 140px 140px 70px';

export function TokensPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const t = useT();
  const copy = useCopy();
  const fmt = useFormat();
  const schema = useMemo(() => tokenFormSchema(t), [t]);

  const tokens = useTokens(org);
  const createToken = useCreateToken(org);
  const revokeToken = useRevokeToken(org);

  const [createOpen, setCreateOpen] = useState(false);
  const [justCreated, setJustCreated] = useState<string | null>(null);

  const form = useForm<TokenFormValues>({ resolver: zodResolver(schema), defaultValues: { name: '', role: 'viewer' } });

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
            <span className="truncate text-xs font-semibold">{token.name}</span>
            <RoleBadge role={token.role} />
            <span className="text-[11px] text-muted-foreground">
              {token.last_used_at ? fmt.date(token.last_used_at) : t.common.never}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {token.expires_at ? fmt.date(token.expires_at) : t.common.never}
            </span>
            <button
              type="button"
              className="text-[11px] text-destructive"
              onClick={async () => {
                if (!confirm(t.tokens.revokeConfirm(token.name))) return;
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
          </GridRow>
        ))}
        {(tokens.data?.tokens ?? []).length === 0 && (
          <div className="p-4 text-xs text-muted-foreground">{t.tokens.empty}</div>
        )}
      </Card>

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
                const res = await createToken.mutateAsync(values);
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
                    onChange={field.onChange}
                  />
                )}
              />
              <span className="text-[11px] text-muted-foreground">
                {t.tokens.roleHint}
              </span>
            </div>
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
