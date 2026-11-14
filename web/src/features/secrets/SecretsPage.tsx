import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useDeleteSecret, useSecrets } from '../../api/hooks/useSecrets.ts';
import type { OrgSecret } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { conflictChecks } from './conflictChecks.ts';
import { SecretDialog } from './SecretDialog.tsx';
import { SecretRow } from './SecretRow.tsx';

const COLUMNS = 'minmax(0, 0.9fr) minmax(0, 1.1fr) 110px minmax(0, 1fr) 40px';

/**
 * SecretsPage is Settings → Secrets (backlog 3.13d): the organization's
 * secret store, the credentials the checks the Pulsix server runs need
 * (an SNMP community or login). Every member sees the names (checks.read);
 * creating, replacing and deleting need credentials.write, the bar of the
 * other stored credentials. A value is never shown: the API returns none.
 */
export function SecretsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const s = t.secrets;
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'credentials.write');
  const secrets = useSecrets(org);
  const remove = useDeleteSecret(org);
  const [editing, setEditing] = useState<OrgSecret | 'new' | null>(null);
  const [blocked, setBlocked] = useState<OrgSecret | null>(null);

  const guard = useQueryGuard(secrets);
  if (guard) return guard;
  if (!secrets.data) return <QueryLoading />;
  const list = secrets.data.secrets ?? [];

  async function onDelete(secret: OrgSecret) {
    // The list already knows the checks naming it: say so instead of
    // asking for a delete the server would refuse. The 409 below stays for
    // a list that went stale.
    if ((secret.used_by ?? []).length > 0) {
      setBlocked(secret);
      return;
    }
    if (!(await ask({ message: s.deleteConfirm(secret.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(secret.name);
      toast.show(s.deleted(secret.name));
    } catch (err) {
      const checks = conflictChecks(err);
      if (checks) toast.show(s.inUse(secret.name, checks.join(', '), checks.length), 'destructive');
      else toast.show(err instanceof Error ? err.message : s.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{s.title}</h1>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <KeyRound size={15} /> {s.add}
          </Button>
        )}
      </div>
      <span className="text-[13px] text-muted-foreground">{s.subtitle}</span>
      {orgInfo.data && !canWrite && <p className="text-xs text-muted-foreground">{s.readOnly}</p>}
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{s.columns.name}</span>
          <span>{s.columns.fields}</span>
          <span>{s.columns.updated}</span>
          <span>{s.columns.usedBy}</span>
          <span />
        </GridHeader>
        {list.map((secret) => (
          <SecretRow
            key={secret.name}
            secret={secret}
            columns={COLUMNS}
            canWrite={canWrite}
            onReplace={setEditing}
            onDelete={(x) => void onDelete(x)}
          />
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{s.empty}</p>}
      </Card>
      {list.length > 0 && <span className="text-[11px] text-muted-foreground">{s.count(list.length)}</span>}
      {blocked && (
        <Dialog open onClose={() => setBlocked(null)} title={s.inUseTitle}>
          <div className="flex flex-col gap-4">
            <p className="text-sm">
              {s.inUseBody(blocked.name, (blocked.used_by ?? []).join(', '), (blocked.used_by ?? []).length)}
            </p>
            <div className="flex justify-end">
              <Button variant="primary" onClick={() => setBlocked(null)}>
                {s.close}
              </Button>
            </div>
          </div>
        </Dialog>
      )}
      {editing && (
        <SecretDialog
          org={org}
          secret={editing === 'new' ? undefined : editing}
          taken={list.map((x) => x.name)}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}
