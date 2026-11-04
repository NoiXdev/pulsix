import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { useOrg, useUpdateOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Card } from '../../ui/table.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * OrgGeneralPage is Settings → General: the organization's name, which an
 * owner or admin (org.manage) may change, and its slug, which is fixed
 * because every URL of the organization carries it.
 */
export function OrgGeneralPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const orgInfo = useOrg(org);
  const update = useUpdateOrg(org);
  const [draft, setDraft] = useState<string | null>(null);

  const guard = useQueryGuard(orgInfo);
  if (guard) return guard;
  if (!orgInfo.data) return <QueryLoading />;

  const current = orgInfo.data.org;
  const canManage = may(current.role, 'org.manage');
  const name = draft ?? current.name;
  const changed = name.trim() !== current.name && name.trim() !== '';

  return (
    <>
      <h1 className="text-xl font-semibold">{t.settings.general.title}</h1>
      <span className="text-[13px] text-muted-foreground">{t.settings.general.hint}</span>

      <div className="grid grid-cols-1 items-start gap-4 min-[1100px]:grid-cols-2">
        <Card>
          <form
            className="flex flex-col gap-3 p-4"
            onSubmit={async (e) => {
              e.preventDefault();
              if (!canManage || !changed) return;
              try {
                await update.mutateAsync(name.trim());
                setDraft(null);
                toast.show(t.settings.general.saved);
              } catch (err) {
                toast.show(err instanceof Error ? err.message : t.settings.general.saveFailed, 'destructive');
              }
            }}
          >
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="org_name">{t.settings.general.name}</Label>
              <Input
                id="org_name"
                value={name}
                readOnly={!canManage}
                onChange={(e) => setDraft(e.target.value)}
              />
              {!canManage && <span className="text-xs text-muted-foreground">{t.settings.general.readOnlyHint}</span>}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="org_slug">{t.settings.general.slug}</Label>
              <Input id="org_slug" value={current.slug} readOnly className="font-mono" />
              <span className="text-xs text-muted-foreground">{t.settings.general.slugHint}</span>
            </div>
            {canManage && (
              <div className="flex justify-end">
                <Button type="submit" variant="primary" disabled={!changed || update.isPending}>
                  {t.settings.general.save}
                </Button>
              </div>
            )}
          </form>
        </Card>
      </div>
    </>
  );
}
