import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { z } from 'zod';
import { KeyRound } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { RoleBadge } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateToken, useRevokeToken, useTokens } from '../../api/hooks/useTokens.ts';

const tokenFormSchema = z.object({
  name: z.string().min(1, 'required'),
  role: z.enum(['viewer', 'operator', 'admin', 'owner']),
});
type TokenFormValues = z.infer<typeof tokenFormSchema>;

const COLUMNS = 'minmax(0, 1fr) 100px 140px 140px 70px';

export function TokensPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();

  const tokens = useTokens(org);
  const createToken = useCreateToken(org);
  const revokeToken = useRevokeToken(org);

  const [createOpen, setCreateOpen] = useState(false);
  const [justCreated, setJustCreated] = useState<string | null>(null);

  const form = useForm<TokenFormValues>({ resolver: zodResolver(tokenFormSchema), defaultValues: { name: '', role: 'viewer' } });

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-xl font-semibold">API tokens</h1>
          <span className="text-[13px] text-muted-foreground">
            {tokens.data ? `${(tokens.data.tokens ?? []).length} tokens` : 'Loading…'}
          </span>
        </div>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <KeyRound size={15} /> Create token
        </Button>
      </div>

      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Name</span>
          <span>Role</span>
          <span>Last used</span>
          <span>Expires</span>
          <span />
        </GridHeader>
        {(tokens.data?.tokens ?? []).map((t) => (
          <GridRow key={t.id} columns={COLUMNS}>
            <span className="truncate text-xs font-semibold">{t.name}</span>
            <RoleBadge role={t.role} />
            <span className="text-[11px] text-muted-foreground">
              {t.last_used_at ? new Date(t.last_used_at).toLocaleDateString() : 'never'}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {t.expires_at ? new Date(t.expires_at).toLocaleDateString() : 'never'}
            </span>
            <button
              type="button"
              className="text-[11px] text-destructive"
              onClick={async () => {
                if (!confirm(`Revoke token ${t.name}?`)) return;
                await revokeToken.mutateAsync(t.id);
                toast.show(`Token ${t.name} revoked`);
              }}
            >
              revoke
            </button>
          </GridRow>
        ))}
        {(tokens.data?.tokens ?? []).length === 0 && (
          <div className="p-4 text-xs text-muted-foreground">No tokens yet.</div>
        )}
      </Card>

      <Dialog
        open={createOpen}
        onClose={() => {
          setCreateOpen(false);
          setJustCreated(null);
          form.reset();
        }}
        title="Create token"
      >
        {justCreated ? (
          <div className="flex flex-col gap-3">
            <p className="text-xs text-muted-foreground">
              Copy this token now — it is shown exactly once and cannot be retrieved again.
            </p>
            <code className="break-all rounded-md border border-border bg-background p-3 font-mono text-xs">
              {justCreated}
            </code>
            <div className="flex justify-end gap-2">
              <Button
                onClick={() => navigator.clipboard.writeText(justCreated)}
              >
                Copy
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  setCreateOpen(false);
                  setJustCreated(null);
                  form.reset();
                }}
              >
                Done
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
                toast.show(err instanceof Error ? err.message : 'failed to create token', 'destructive');
              }
            })}
          >
            <div className="flex flex-col gap-1.5">
              <Label>Name</Label>
              <Input {...form.register('name')} placeholder="ci-deploy" />
              {form.formState.errors.name && (
                <span className="text-[11px] text-destructive">{form.formState.errors.name.message}</span>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>Role</Label>
              <select {...form.register('role')} className="h-8 rounded-md border border-border bg-background px-2.5 text-xs">
                <option value="viewer">viewer</option>
                <option value="operator">operator</option>
                <option value="admin">admin</option>
                <option value="owner">owner</option>
              </select>
              <span className="text-[11px] text-muted-foreground">
                Capped at your own role — the server never mints a token more privileged than you.
              </span>
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" disabled={createToken.isPending}>
                Create token
              </Button>
            </div>
          </form>
        )}
      </Dialog>
    </>
  );
}
